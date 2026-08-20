package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Named states, taken and put back while the emulator runs.
//
// `serve --state <file>` already persisted the store, but only twice per
// session: it restores at boot and writes at exit. That covers "keep my work
// between runs" and nothing else. What it cannot do is the thing a test author
// actually asks for — reach a fixture state once, name it, and come back to it
// in a second, as many times as the test needs.
//
// So no new storage was added. The store could already write and read itself;
// what was missing was a way to reach the store of a process that is already
// answering. That is GET and PUT on /_feint/state, and this file is the client.
// A snapshot is therefore exactly what `--state` writes, and the two are
// interchangeable by construction rather than by promise.

// snapshotTimeout is generous compared to the status calls: a large store is a
// large body, and a snapshot that fails because a fixture grew is worse than one
// that takes a second.
const snapshotTimeout = 30 * time.Second

// maxStateBody mirrors the limit the emulator applies to a snapshot it accepts
// (emulator.maxStateBody). Stated here rather than imported: the CLI talks to an
// emulator over HTTP, possibly an older one, so it bounds what it reads on its
// own terms.
const maxStateBody = 64 << 20

func snapshot(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "feint: snapshot needs a subcommand: save, load, list or rm")
		return exitError
	}
	switch args[0] {
	case "save":
		return snapshotSave(args[1:], stdout, stderr)
	case "load":
		return snapshotLoad(args[1:], stdout, stderr)
	case "list", "ls":
		return snapshotList(args[1:], stdout, stderr)
	case "rm", "delete":
		return snapshotRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "feint: unknown snapshot subcommand %q; expected save, load, list or rm\n", args[0])
		return exitError
	}
}

// snapshotName takes the name before flag parsing, for the reason `env`
// documents: the standard flag package stops at the first non-flag argument, so
// `snapshot save fixture --addr …` would leave the address unparsed.
//
// The name is validated rather than trusted. It becomes a file path, and a name
// carrying a separator or a parent reference writes wherever the caller wants —
// including over a file this tool has no business touching. Refused outright
// instead of sanitised: a name that has to be rewritten to be safe is a name the
// caller did not mean.
func snapshotName(args []string, stderr io.Writer, verb string) (string, []string, bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "feint: snapshot %s needs a name: feint snapshot %s <name> [flags]\n", verb, verb)
		return "", nil, false
	}
	name := args[0]
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, "-") {
		fmt.Fprintf(stderr, "feint: %q is not a snapshot name: no path separators, no . or ..\n", name)
		return "", nil, false
	}
	return name, args[1:], true
}

// snapshotDir is where named states live. A snapshot outlives the instance that
// produced it, and is meant to be loaded into a different one.
//
// It deliberately does not use stateHome(), which prefers XDG_RUNTIME_DIR
// because that is the right home for a pidfile: cleaned on logout, so a stale
// record cannot survive a reboot. Applying the same rule here made the whole
// feature evaporate at logout — a fixture is exactly the thing that must still
// be there tomorrow. So snapshots follow XDG_STATE_HOME, whose specification
// says it is for data that should persist between restarts.
func snapshotDir() (string, error) {
	home, err := persistentHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, snapshotsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

func snapshotPath(name string) (string, error) {
	dir, err := snapshotDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// stateURL is the state endpoint of a running emulator, built from a listen
// address the same way healthURL is.
func stateURL(addr string) string {
	return strings.Replace(healthURL(addr), "/health", "/state", 1)
}

func snapshotSave(args []string, stdout, stderr io.Writer) int {
	name, rest, ok := snapshotName(args, stderr, "save")
	if !ok {
		return exitError
	}
	fs := newFlagSet("snapshot save")
	addr := fs.String("addr", DefaultAddr, "address of the running emulator")
	force := fs.Bool("force", false, "overwrite an existing snapshot of that name")
	if err := fs.Parse(rest); err != nil {
		return exitError
	}

	path, err := snapshotPath(name)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	// Checked before the call rather than after: refusing at the end would mean
	// having asked a running emulator for its whole state to throw it away.
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(stderr, "feint: snapshot %q already exists (%s); pass --force to overwrite\n", name, path)
		return exitError
	}

	body, err := fetchState(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	// Checked before writing: a 200 from something that is not this emulator
	// used to be saved happily, exit 0, and fail only at load — with the file
	// already on disk and the operator believing they had a fixture.
	if countResources(body) < 0 {
		fmt.Fprintf(stderr, "feint: %s answered something that is not a snapshot; is that address an emulator?\n", *addr)
		return exitError
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		fmt.Fprintf(stderr, "feint: write %s: %v\n", path, err)
		return exitError
	}
	fmt.Fprintf(stdout, "saved %d resources to %s\n", countResources(body), path)
	return exitOK
}

func snapshotLoad(args []string, stdout, stderr io.Writer) int {
	name, rest, ok := snapshotName(args, stderr, "load")
	if !ok {
		return exitError
	}
	fs := newFlagSet("snapshot load")
	addr := fs.String("addr", DefaultAddr, "address of the running emulator")
	if err := fs.Parse(rest); err != nil {
		return exitError
	}

	path, err := snapshotPath(name)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	body, err := os.ReadFile(path) //nolint:gosec // path is built from a validated name under the state home
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "feint: no snapshot named %q; feint snapshot list shows what there is\n", name)
			return exitError
		}
		fmt.Fprintf(stderr, "feint: read %s: %v\n", path, err)
		return exitError
	}

	// The emulator replaces its whole store with what it is given, so loading is
	// destructive by design. Said plainly here because the alternative — merging —
	// would make a fixture depend on what the session did before it, which is the
	// one thing a fixture exists to avoid.
	count, err := putState(*addr, body)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "loaded %s: the emulator now holds %d resources\n", name, count)
	return exitOK
}

func snapshotList(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("snapshot list")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	dir, err := snapshotDir()
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "feint: read %s: %v\n", dir, err)
		return exitError
	}

	type snapshotView struct {
		Name      string `json:"name"`
		Resources int    `json:"resources"`
		Bytes     int64  `json:"bytes"`
		Saved     string `json:"saved_at"`
		Path      string `json:"path"`
	}
	out := make([]snapshotView, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Read to count: the number of resources is what makes a listing worth
		// reading, and a snapshot small enough to keep on disk is small enough
		// to count. A file that cannot be parsed is listed with -1 rather than
		// hidden, because a corrupt snapshot the operator cannot see is one they
		// will keep trying to load.
		body, err := os.ReadFile(path) //nolint:gosec // entries of a directory this tool owns
		count := -1
		if err == nil {
			count = countResources(body)
		}
		out = append(out, snapshotView{
			Name:      strings.TrimSuffix(e.Name(), ".json"),
			Resources: count,
			Bytes:     info.Size(),
			Saved:     info.ModTime().UTC().Format(time.RFC3339),
			Path:      path,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	switch *format {
	case "json":
		if err := writeJSON(stdout, out); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		return exitOK
	case "text":
	default:
		// Refused rather than silently falling back to text, which is what the
		// catalog command does and what a script piping into jq needs: a typo in
		// --format must not produce a table nobody can parse.
		fmt.Fprintf(stderr, "feint: unknown format %q; expected text or json\n", *format)
		return exitError
	}
	if len(out) == 0 {
		fmt.Fprintf(stdout, "no snapshots in %s\n", dir)
		return exitOK
	}
	fmt.Fprintf(stdout, "%-24s %10s %10s  %s\n", "NAME", "RESOURCES", "BYTES", "SAVED")
	for _, s := range out {
		resources := itoa(s.Resources)
		if s.Resources < 0 {
			resources = "unreadable"
		}
		fmt.Fprintf(stdout, "%-24s %10s %10d  %s\n", s.Name, resources, s.Bytes, s.Saved)
	}
	return exitOK
}

func snapshotRemove(args []string, stdout, stderr io.Writer) int {
	name, rest, ok := snapshotName(args, stderr, "rm")
	if !ok {
		return exitError
	}
	if len(rest) != 0 {
		fmt.Fprintf(stderr, "feint: unexpected argument %q; snapshot rm takes one name\n", rest[0])
		return exitError
	}
	path, err := snapshotPath(name)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "feint: no snapshot named %q\n", name)
			return exitError
		}
		fmt.Fprintf(stderr, "feint: remove %s: %v\n", path, err)
		return exitError
	}
	fmt.Fprintf(stdout, "removed %s\n", name)
	return exitOK
}

// fetchState reads the whole store of a running emulator.
//
// The error says what to do about it, because the failure everyone hits is the
// same one: no emulator is running, and "connection refused" does not say which
// command starts it.
func fetchState(addr string) ([]byte, error) {
	client := &http.Client{Timeout: snapshotTimeout}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, stateURL(addr), nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no emulator answering on %s: %w\n       start one with: feint start --addr %s", addr, err, addr)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", stateURL(addr), res.StatusCode)
	}
	// Bounded like the server bounds its input. Without it, a wrong --addr
	// pointing at something that streams forever grows this process until the
	// machine notices, and the asymmetry (server bounded, client not) is the
	// kind that only shows up on the day it matters.
	body, err := io.ReadAll(io.LimitReader(res.Body, maxStateBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxStateBody {
		return nil, fmt.Errorf("%s answered with more than %d bytes; that is not a snapshot this version writes", stateURL(addr), maxStateBody)
	}
	return body, nil
}

// putState replaces the store of a running emulator and returns what it then
// holds, as the emulator itself counts it.
func putState(addr string, body []byte) (int, error) {
	client := &http.Client{Timeout: snapshotTimeout}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, stateURL(addr), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("no emulator answering on %s: %w\n       start one with: feint start --addr %s", addr, err, addr)
	}
	defer func() { _ = res.Body.Close() }()

	var answer struct {
		Status    string `json:"status"`
		Resources int    `json:"resources"`
		Error     string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		return 0, fmt.Errorf("%s answered %d with a body this version cannot read", stateURL(addr), res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the emulator refused the snapshot: %s", answer.Error)
	}
	return answer.Resources, nil
}

// countResources counts the entries of a snapshot without giving them a type.
//
// Raw messages, deliberately: counting must not become a second place that knows
// what a resource looks like. But it does have to know the envelope, and that is
// the whole point of #133 — this function read a bare array, so the day the
// header appeared it would have answered -1 for every snapshot ever written,
// silently, because its own error path is a sentinel rather than a failure.
func countResources(body []byte) int {
	var file struct {
		Resources []json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		return -1
	}
	return len(file.Resources)
}

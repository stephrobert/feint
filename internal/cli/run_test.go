package cli

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/providers/scaleway"
	"github.com/stephrobert/feint/internal/s3"
)

// reportServer serves one validated S3 report over loopback, which is what
// fetchS3 requires and therefore what `feint run` discovers its doors from.
func reportServer(t *testing.T, doors ...s3.Door) *httptest.Server {
	t.Helper()
	report := s3.Report{SchemaVersion: s3.ReportVersion, Enabled: len(doors) != 0,
		MaxBlobBytes: 1 << 20, MaxObjectBytes: 1 << 10,
		Doors: doors, HTTPRequests: map[string]uint64{}}
	if report.Doors == nil {
		report.Doors = []s3.Door{}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		if err := json.NewEncoder(w).Encode(report); err != nil {
			t.Errorf("encode report: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func door(provider, region string) s3.Door {
	return s3.Door{Provider: provider, Region: region, Path: s3.DoorPath(provider, region)}
}

// TestRunDiscoversItsDoorsAndNeverTakesAHostnameFromTheOperator is the
// accepting half: a mounted door becomes an interception whose target is the
// door's own canonical path, and nothing in that mapping was typed.
func TestRunDiscoversItsDoorsAndNeverTakesAHostnameFromTheOperator(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"), door("exoscale", "ch-gva-2"))
	profiles, err := runProfiles(server.URL)
	if err != nil {
		t.Fatalf("mounted doors refused: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	want := map[string]string{
		"s3.fr-par.scw.cloud": "scaleway/fr-par",
		"sos-ch-gva-2.exo.io": "exoscale/ch-gva-2",
	}
	for _, profile := range profiles {
		scope, ok := want[profile.CloudHost]
		if !ok || scope != profile.Provider+"/"+profile.Region {
			t.Fatalf("authority %q is not the one the pack declares for %s/%s",
				profile.CloudHost, profile.Provider, profile.Region)
		}
	}
	// The transport the command builds must accept exactly these, which is the
	// property that makes an off-loopback target unspellable: the only targets
	// are doors the emulator reported.
	if _, err := s3.NewLocalTransport(server.URL, profiles); err != nil {
		t.Fatalf("discovered profiles rejected by the local transport: %v", err)
	}
}

// TestRunRefusesAnEmulatorWithNoDoorAndSaysWhatToStart keeps the refusal
// actionable: an operator who forgot --objectstore reads what to add, not that
// something is unavailable.
func TestRunRefusesAnEmulatorWithNoDoorAndSaysWhatToStart(t *testing.T) {
	server := reportServer(t)
	_, err := runProfiles(server.URL)
	if err == nil {
		t.Fatal("a doorless emulator was accepted")
	}
	for _, mention := range []string{"--objectstore", "--s3-doors"} {
		if !strings.Contains(err.Error(), mention) {
			t.Fatalf("the refusal does not name %s: %v", mention, err)
		}
	}
}

// authorityLessPack is a Scaleway pack that declares no measured cloud
// authority, which is the one case runProfiles must refuse rather than guess.
type authorityLessPack struct{ emulator.Pack }

func (authorityLessPack) ObjectStorage(region string) s3.Profile {
	return s3.Profile{Provider: "scaleway", Region: region}
}

// TestRunRefusesADoorWithNoMeasuredAuthority is cited by runProfiles. Without
// its check, the command would hand s3.NewLocalTransport an empty authority and
// fail with a message about transports rather than about the door.
func TestRunRefusesADoorWithNoMeasuredAuthority(t *testing.T) {
	previous := packsFor
	packsFor = func(env *emulator.Env) ([]emulator.Pack, error) {
		return []emulator.Pack{authorityLessPack{Pack: scaleway.New(env)}}, nil
	}
	t.Cleanup(func() { packsFor = previous })

	server := reportServer(t, door("scaleway", "fr-par"))
	_, err := runProfiles(server.URL)
	if err == nil {
		t.Fatal("a door with no measured authority was accepted")
	}
	if !strings.Contains(err.Error(), "scaleway/fr-par") {
		t.Fatalf("the refusal does not name the door: %v", err)
	}
}

// TestRunRefusesCredentialsThatAreNotKnownFakesByNameOnly holds both halves of
// the fail-closed rule, and the one that matters most: the NAME is reported and
// the value never is, because this output lands in terminals and bug reports.
func TestRunRefusesCredentialsThatAreNotKnownFakesByNameOnly(t *testing.T) {
	const secret = "AKIAREALLOOKINGKEY42"
	offenders := realCredentials([]string{
		"PATH=/usr/bin",
		"SCW_ACCESS_KEY=" + knownFakes[2],
		"AWS_SECRET_ACCESS_KEY=",
		"OSC_ACCESS_KEY=" + secret,
	})
	if len(offenders) != 1 || offenders[0] != "OSC_ACCESS_KEY" {
		t.Fatalf("got %v, want only OSC_ACCESS_KEY", offenders)
	}
	if none := realCredentials([]string{"SCW_SECRET_KEY=" + knownFakes[3]}); len(none) != 0 {
		t.Fatalf("a known fake was refused: %v", none)
	}

	var stdout, stderr bytes.Buffer
	t.Setenv("OSC_ACCESS_KEY", secret)
	if code := runCommand([]string{"--", "true"}, &stdout, &stderr); code != exitError {
		t.Fatalf("the run was not refused: code %d", code)
	}
	if strings.Contains(stderr.String(), secret) || strings.Contains(stdout.String(), secret) {
		t.Fatal("the credential value was printed")
	}
	if !strings.Contains(stderr.String(), "OSC_ACCESS_KEY") {
		t.Fatalf("the refusal does not name the variable: %q", stderr.String())
	}
}

// TestRunRemovesItsAuthorityAfterASignal is cited by runChild. The child sends
// SIGTERM to this test process, which is the real path: runChild arms
// signal.Notify before starting the child, so the signal is relayed rather than
// killing the test, and the deferred cleanup is what this asserts.
//
// Which half catches which, because getting this backwards is how a test comes
// to be believed for the wrong reason: the deferred removal would run even
// without the relay, so the file check does NOT hold the relay. What holds it
// is the exit code — a child that never receives the signal sleeps out and
// reports success. The file check holds the cleanup, including on this path.
func TestRunRemovesItsAuthorityAfterASignal(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"))
	profiles, err := runProfiles(server.URL)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	// `kill -TERM $PPID` targets this test process; runChild relays it to the
	// child, which then dies on it rather than sleeping out the timeout.
	code, err := runChild(server.URL, "127.0.0.1:0", profiles,
		[]string{"sh", "-c", "kill -TERM $PPID; sleep 30"}, false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if code == 0 {
		t.Fatal("the child reported success after being terminated")
	}

	// The receipt names the authority, which is how its removal is observable
	// at all — the directory is private to the run.
	var authority string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if _, after, ok := strings.Cut(line, "authority at "); ok {
			authority = strings.TrimSpace(after)
		}
	}
	if authority == "" {
		t.Fatalf("the receipt does not name the authority: %q", stdout.String())
	}
	if _, err := os.Stat(authority); !os.IsNotExist(err) {
		t.Fatalf("the authority survived the signal at %s: %v", authority, err)
	}
}

// TestRunKeepsItsAuthorityOwnerOnly is cited by runChild, and it asks the
// question from where it matters: inside the child, through the variable the
// child was actually given. A test that stat'd a path this test computed would
// be checking its own arithmetic.
func TestRunKeepsItsAuthorityOwnerOnly(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"))
	profiles, err := runProfiles(server.URL)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code, err := runChild(server.URL, "127.0.0.1:0", profiles,
		// POSIX only, and read rather than stat'd: `stat -c` is GNU, and the
		// BSD stat on macOS rejected it, which failed this test on a property
		// it was not measuring. `cat` to /dev/null proves the child can READ
		// its authority (exit 0 below), and `ls -ld` prints the directory's
		// mode the one way every Unix spells it.
		[]string{"sh", "-c", `cat "$SSL_CERT_FILE" > /dev/null || exit 3; printf 'authority=%s\n' "$SSL_CERT_FILE"; ls -ld "$(dirname "$SSL_CERT_FILE")"`},
		false, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("the child could not read its own authority: code %d, %v, %q", code, err, stderr.String())
	}
	// Two properties, and they pull in opposite directions, which is why both
	// are asserted: the directory must be owner-only, and the CA inside it must
	// still be READABLE by the client process. proxy.WriteCA writes 0644 for
	// that second reason, deliberately; the privacy comes from the directory,
	// and the authority's private key is never written at all.
	if !strings.Contains(stdout.String(), "drwx------") {
		t.Fatalf("the child saw a directory mode other than owner-only: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ca.pem") {
		t.Fatalf("the child could not name its own authority: %q", stdout.String())
	}
}

// deadPID returns a process identifier that has certainly exited and been
// reaped, which is what a leftover directory's owner file holds.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("could not produce an exited process: %v", err)
	}
	return cmd.Process.Pid
}

func writeRunDir(t *testing.T, root, name string, pid int) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	if pid == 0 {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, runOwnerFile),
		[]byte(fmt.Sprintln(pid)), 0o600); err != nil {
		t.Fatalf("write the owner file: %v", err)
	}
}

// TestDoctorTellsALeftoverAuthorityFromARunInFlight is cited by
// checkLeftoverRunAuthorities, and the distinction is the whole reason the
// check exists: a healthy `feint run` owns one of these directories for as long
// as its child lives, so a check that merely reported their existence would
// warn on every correct use.
func TestDoctorTellsALeftoverAuthorityFromARunInFlight(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	if os.TempDir() != root {
		t.Skipf("this platform does not take TMPDIR, so the scan cannot be pointed at a fixture")
	}

	if c := checkLeftoverRunAuthorities(); c.state != verdictOK ||
		!strings.Contains(c.title, "no interception authority") {
		t.Fatalf("an empty directory did not read as clean: %+v", c)
	}

	// A run in flight: this test's own process owns it, and it must NOT be
	// reported as something to remove.
	writeRunDir(t, root, runDirPrefix+"live", os.Getpid())
	live := checkLeftoverRunAuthorities()
	if live.state != verdictOK || !strings.Contains(live.title, "in flight") {
		t.Fatalf("a live run was reported as a leftover: %+v", live)
	}
	if live.fix != "" {
		t.Fatalf("a live run was given something to fix: %q", live.fix)
	}

	// A leftover: an owner that has exited.
	writeRunDir(t, root, runDirPrefix+"dead", deadPID(t))
	stale := checkLeftoverRunAuthorities()
	if stale.state != verdictWarn {
		t.Fatalf("a leftover was not reported as a warning: %+v", stale)
	}
	if !strings.Contains(stale.detail, runDirPrefix+"dead") {
		t.Fatalf("the leftover is not named: %+v", stale)
	}
	if strings.Contains(stale.detail, runDirPrefix+"live") &&
		!strings.Contains(stale.detail, "left alone") {
		t.Fatalf("the live run was listed among the leftovers: %+v", stale)
	}

	// An owner file an older version never wrote counts as dead, which is the
	// direction that gets a leftover removed rather than hidden.
	writeRunDir(t, root, runDirPrefix+"ownerless", 0)
	if c := checkLeftoverRunAuthorities(); !strings.Contains(c.detail, runDirPrefix+"ownerless") {
		t.Fatalf("a directory with no owner file was not reported: %+v", c)
	}
}

// TestDoctorSeesARunInFlightAsItsOwner joins the two halves, and nothing else
// does: TestDoctorTellsALeftoverAuthorityFromARunInFlight writes its own
// fixtures, so it would still pass if `feint run` stopped recording an owner
// altogether. This drives a real run and asks the diagnostic about it while the
// child is alive, which is the only arrangement that proves the producer and
// the check agree on both the prefix and the owner file.
func TestDoctorSeesARunInFlightAsItsOwner(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	if os.TempDir() != root {
		t.Skipf("this platform does not take TMPDIR, so the run cannot be pointed at a fixture")
	}
	server := reportServer(t, door("scaleway", "fr-par"))
	profiles, err := runProfiles(server.URL)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}

	release := filepath.Join(root, "release")
	done := make(chan error, 1)
	var stdout, stderr syncBuffer
	go func() {
		_, err := runChild(server.URL, "127.0.0.1:0", profiles,
			[]string{"sh", "-c", `while [ ! -f "$0" ]; do sleep 0.02; done`, release},
			false, &stdout, &stderr)
		done <- err
	}()
	defer func() {
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Errorf("release the child: %v", err)
		}
		if err := <-done; err != nil {
			t.Errorf("the run failed: %v (%q)", err, stderr.String())
		}
	}()

	// Wait for the run to own its directory, then ask the diagnostic.
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := checkLeftoverRunAuthorities()
		if strings.Contains(c.title, "in flight") {
			if c.state != verdictOK {
				t.Fatalf("a run in flight was not reported as healthy: %+v", c)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the diagnostic never saw the run in flight; last verdict: %+v", c)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRunInterceptsBucketsAddressedAsSubdomains is cited by runChild, and it
// exists because the first proof of this command missed the case entirely: curl
// against the bare authority is path-style, so it passed while every
// virtual-host bucket request — the form Terraform uses — would have been
// refused and never tunnelled.
//
// It goes THROUGH the proxy on purpose. A first draft asserted the receipt text
// and the transport's routing, and neither touches the forward's allow-list:
// the mutation that drops the wildcard would have left it green. So this drives
// a real HTTPS request across the CONNECT, with the run's own authority, which
// is the only arrangement where the allow-list is the thing under test.
func TestRunInterceptsBucketsAddressedAsSubdomains(t *testing.T) {
	door := door("scaleway", "fr-par")
	store, objects := doorServer(t, door)
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close the byte store: %v", err)
		}
	}()
	profiles, err := runProfiles(objects.URL)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}

	release := filepath.Join(t.TempDir(), "release")
	done := make(chan error, 1)
	var stdout, stderr syncBuffer
	go func() {
		_, err := runChild(objects.URL, "127.0.0.1:0", profiles,
			[]string{"sh", "-c", `while [ ! -f "$0" ]; do sleep 0.02; done`, release},
			false, &stdout, &stderr)
		done <- err
	}()
	defer func() {
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Errorf("release the child: %v", err)
		}
		if err := <-done; err != nil {
			t.Errorf("the run failed: %v (%q)", err, stderr.String())
		}
	}()

	proxyURL, ca := awaitReceipt(t, &stdout)
	pem, err := os.ReadFile(ca) //nolint:gosec // the path this run printed
	if err != nil {
		t.Fatalf("read the run's authority: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the run's authority is not a usable certificate pool")
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	defer client.CloseIdleConnections()

	// The subject: a bucket addressed as a subdomain reaches the door and the
	// door answers about that bucket. 404 NoSuchBucket is a door answer; a
	// refused CONNECT is a transport error and never a status.
	for _, probe := range []struct {
		url    string
		status int
	}{
		{url: "https://s3.fr-par.scw.cloud/", status: http.StatusOK},
		{url: "https://feint-terraform-storage.s3.fr-par.scw.cloud/", status: http.StatusNotFound},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, probe.url, nil)
		if err != nil {
			t.Fatalf("build %s: %v", probe.url, err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s never reached the door: %v", probe.url, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		if err := response.Body.Close(); err != nil {
			t.Errorf("close %s: %v", probe.url, err)
		}
		if readErr != nil {
			t.Fatalf("read %s: %v", probe.url, readErr)
		}
		if response.StatusCode != probe.status {
			t.Fatalf("%s answered %d, want %d: %s", probe.url, response.StatusCode, probe.status, body)
		}
	}

	// An authority of another region stays refused: the widening is one label
	// under a declared door, never a second door.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"https://s3.nl-ams.scw.cloud/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Do(request); err == nil {
		_ = response.Body.Close()
		t.Fatal("an undeclared region was served; it must be refused, never tunnelled")
	}
}

// awaitReceipt reads the proxy address and authority path out of the receipt,
// which is also the only place they are published — the run owns both.
func awaitReceipt(t *testing.T, stdout *syncBuffer) (*url.URL, string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, line := range strings.Split(stdout.String(), "\n") {
			rest, ok := strings.CutPrefix(line, "feint run: proxy on ")
			if !ok {
				continue
			}
			address, authority, split := strings.Cut(rest, ", authority at ")
			if !split {
				continue
			}
			parsed, err := url.Parse(strings.TrimSpace(address))
			if err != nil {
				t.Fatalf("the receipt names an unusable proxy %q: %v", address, err)
			}
			return parsed, strings.TrimSpace(authority)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run printed no receipt: %q", stdout.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// doorServer mounts a real S3 door over a private byte store, so a request that
// crosses the proxy is answered by the thing `feint run` exists to reach.
func doorServer(t *testing.T, d s3.Door) (*objectstore.Store, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("restrict the byte store: %v", err)
	}
	store, err := objectstore.OpenWithLimit(t.Context(), filepath.Join(root, "bytes"), 16<<20)
	if err != nil {
		t.Fatalf("open the byte store: %v", err)
	}
	packs, err := packsFor(emulator.DefaultEnv())
	if err != nil {
		t.Fatalf("build the packs: %v", err)
	}
	profiles, err := storageProfiles(packs, d.Provider+"/"+d.Region)
	if err != nil {
		t.Fatalf("profile for %s/%s: %v", d.Provider, d.Region, err)
	}
	handler, err := s3.Mount(http.NotFoundHandler(), store, profiles, 1<<20,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("mount the door: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return store, server
}

// TestRunGivesTheChildOnlyTheFourMeasuredVariables is cited by runChild, and it
// is a list rather than a shape check because the list is the measurement.
//
// The variable it is really about is the one that is ABSENT. An earlier draft
// also set AWS_CA_BUNDLE, reasoning that an S3 client would want it. The
// published Scaleway provider 2.82.0 then failed its apply with "unable to add
// custom RootCAs HTTPClient, has no WithTransportOptions, *http.Client": it
// hands the SDK a prebuilt *http.Client, so a CA bundle the SDK is asked to
// install has nowhere to go. Removing it made the same apply complete, with
// 139,264 identical bytes landing in the local byte store. A Go client already
// trusts SSL_CERT_FILE through crypto/x509, so the variable bought nothing and
// broke the one client this command exists for.
func TestRunGivesTheChildOnlyTheFourMeasuredVariables(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"))
	profiles, err := runProfiles(server.URL)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	// Set in the PARENT, which is the case that matters: an operator whose
	// shell carries it would otherwise hand it to the child, and its bundle
	// does not contain this run's authority under any value.
	t.Setenv("AWS_CA_BUNDLE", "/etc/ssl/certs/ca-certificates.crt")
	var stdout, stderr bytes.Buffer
	// The child reports the environment it was actually given.
	code, err := runChild(server.URL, "127.0.0.1:0", profiles,
		[]string{"sh", "-c", "env"}, false, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("run failed: code %d, %v, %q", code, err, stderr.String())
	}
	given := make(map[string]string)
	for _, line := range strings.Split(stdout.String(), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			given[name] = value
		}
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if !strings.HasPrefix(given[name], "http://127.0.0.1:") {
			t.Fatalf("%s is %q, not a loopback proxy", name, given[name])
		}
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		if !strings.Contains(given[name], "127.0.0.1") {
			t.Fatalf("%s does not exempt the emulator: %q", name, given[name])
		}
	}
	if !strings.HasSuffix(given["SSL_CERT_FILE"], "ca.pem") {
		t.Fatalf("SSL_CERT_FILE is %q", given["SSL_CERT_FILE"])
	}
	if value, set := given["AWS_CA_BUNDLE"]; set {
		t.Fatalf("AWS_CA_BUNDLE is set to %q; the Scaleway provider 2.82.0 fails its apply when it is", value)
	}
}

// syncBuffer is a writer the test can read WHILE the run is still writing to
// it. A plain bytes.Buffer is not that, and -race says so: awaitReceipt polls
// stdout for the receipt line while runChild's goroutine is appending to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

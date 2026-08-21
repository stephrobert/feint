package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/corpus"
	"github.com/stephrobert/feint/internal/trace"
)

// transcriptSanitise turns a recording of a real cloud into an artefact this
// repository may commit (#351).
//
//	feint transcript real.jsonl --sanitise corpus/scaleway/scw.jsonl \
//	  --contract contracts/scaleway.json
//
// One command, and it refuses to write anything it cannot vouch for: the
// sanitised transcript is cross-referenced against the recording it came from
// before it reaches the disk, and a single value of the account surviving into
// it is an error, not a warning. A file that was written and then reported on
// is a file somebody commits.
func transcriptSanitise(exs []trace.Exchange, source, out, contractPath string, stdout, stderr io.Writer) int {
	if contractPath == "" {
		fmt.Fprintln(stderr, "feint: --sanitise needs --contract: which segments of a path are the API's own")
		fmt.Fprintln(stderr, "feint: literals and which are the account's is a question only the provider's document answers")
		return exitError
	}
	if sameFile(source, out) {
		// A sanitiser that can overwrite its own input is a sanitiser that
		// destroys the measurement on a typo, and the recording is the one thing
		// here that cannot be made again without another run against a billed
		// account.
		fmt.Fprintln(stderr, "feint: --sanitise would overwrite the recording it reads; write it somewhere else")
		return exitError
	}
	doc, err := contract.Load(contractPath)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the emulator: %v\n", err)
		return exitError
	}
	vocabulary, code := packVocabulary(packs, stderr)
	if code != exitOK {
		return code
	}

	opt := corpus.Options{Doc: doc, Vocabulary: vocabulary}
	sanitised, rep, err := corpus.Sanitise(exs, opt)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	// Both controls, before the file exists. Audit asks whether anything of the
	// recording survived; Scan asks whether everything in the artefact is
	// something this tool minted or the document publishes. Neither subsumes the
	// other: a value the sanitiser invented wrongly is invisible to the first,
	// and a value that is in both files but allowed is invisible to the second.
	if leaks := corpus.Audit(exs, sanitised, opt); len(leaks) > 0 {
		reportLeaks(stderr, "a value of the recording survived the sanitisation", leaks)
		return exitError
	}
	if leaks := corpus.Scan(sanitised, opt); len(leaks) > 0 {
		reportLeaks(stderr, "the sanitised transcript carries a value it may not", leaks)
		return exitError
	}

	if err := writeTranscript(out, sanitised); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	fmt.Fprintf(stdout, "%d exchange(s) written to %s\n", rep.Exchanges, out)
	fmt.Fprintf(stdout, "%d distinct value(s) replaced by a synthetic one of the same shape\n", rep.Replaced)
	fmt.Fprintf(stdout, "%d value(s) kept: a literal of the API, a word a pack vouches for, a number, a boolean\n", rep.Kept)
	fmt.Fprintln(stdout, "cross-checked against the recording: no value of the account survived")
	if len(rep.Unnamed) > 0 {
		fmt.Fprintf(stdout, "\n%d path(s) this provider's document does not describe, blanked entirely (method and shape kept):\n", len(rep.Unnamed))
		for _, u := range rep.Unnamed {
			fmt.Fprintln(stdout, "  "+u)
		}
		fmt.Fprintln(stdout, "to name them, add the product to tools/contract/<provider>-products.txt and regenerate the contract.")
	}
	return exitOK
}

// packVocabulary gathers what the packs vouch for, holding each declaration to
// its own guard — the wiring half of emulator.UnsafeVocabulary, which is the
// half that is usually missing.
//
// TestThePacksVocabularyPassesItsOwnGuard holds this.
func packVocabulary(packs []emulator.Pack, stderr io.Writer) ([]string, int) {
	var out []string
	unusable := 0
	for _, p := range packs {
		values := emulator.VocabularyOf(p)
		for _, bad := range emulator.UnsafeVocabulary(values) {
			fmt.Fprintf(stderr, "feint: %s vouches for %q, which a sanitised transcript must not keep verbatim\n", p.Name(), bad)
			unusable++
		}
		out = append(out, values...)
	}
	if unusable > 0 {
		return nil, exitError
	}
	return out, exitOK
}

func reportLeaks(stderr io.Writer, headline string, leaks []corpus.Leak) {
	fmt.Fprintf(stderr, "feint: %s; nothing was written.\n", headline)
	const most = 20
	for i, l := range leaks {
		if i == most {
			fmt.Fprintf(stderr, "  … and %d more\n", len(leaks)-most)
			break
		}
		fmt.Fprintln(stderr, "  "+l.String())
	}
}

// writeTranscript writes JSON Lines the way `feint proxy` does, and with the
// same permissions: a corpus is sanitised, not public, until a human has read
// it and committed it.
func writeTranscript(path string, exs []trace.Exchange) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // operator-supplied path
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for i := range exs {
		if err := enc.Encode(exs[i]); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return f.Close()
}

// sameFile reports whether two paths name the same file, following the one case
// a string comparison misses: the same file reached by two paths.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

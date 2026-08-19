package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A stack's own text can defeat every value this CLI prints. The measured case
// (#262, #280): a Terraform configuration pointed here through SCW_API_URL
// carried one scaleway_object_bucket, and that call left for the real
// s3.fr-par.scw.cloud — the provider hardcodes the host for Object Storage —
// while every other resource applied locally. The emulator cannot see a
// request that never reaches it, so the only moment anything can warn is
// before the run, from the configuration's own text.
//
// The division of labour mirrors packEnvHazards: this file reads the files and
// strips what a client would not execute; which tokens mean "this reaches the
// real cloud" is provider knowledge and lives in each pack.

// packStackHazards is the optional half of a pack that can recognise, in the
// text of a Terraform configuration, something measured to reach the real
// cloud no matter where the emulator's exports point. The input is the
// comment-stripped concatenation of the stack's files; the output is one
// warning per recognised signature, empty for a clean stack.
type packStackHazards interface {
	StackHazards(config string) []string
}

// stackFileCap bounds the walk. A Terraform stack is tens of files; the cap
// exists so a `feint env` eval'd from somebody's home directory scans a bounded
// amount and returns, rather than reading a filesystem.
const stackFileCap = 256

// stackConfig gathers the Terraform files under dir — *.tf, *.tf.json and
// *.tofu, recursively, skipping dot-directories such as .git and .terraform —
// into one comment-stripped string. files is how many were read; zero means
// "no stack here" and callers stay silent, which is what keeps this scan free
// of noise for everybody not standing in a stack.
//
// The gate is the directory's own top level: Terraform only runs where root
// module files sit, so a directory without one is not a stack, however many
// projects live somewhere below it. Without this gate, a doctor run from a
// workspace holding vendored provider sources and old stack copies warned
// about text nobody was about to apply — measured on this repository's own
// scratchpad — and a warning that fires on other people's files teaches the
// operator to ignore it. TestADirectoryOfProjectsIsNotAStack fails without
// the gate. Once the gate passes, the walk is recursive on purpose: the root
// module is where the apply happens, and its modules/ subdirectories are part
// of the same run.
func stackConfig(dir string) (config string, files int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0
	}
	rooted := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if name := entry.Name(); strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json") || strings.HasSuffix(name, ".tofu") {
			rooted = true
			break
		}
	}
	if !rooted {
		return "", 0
	}
	var parts []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal: this is a diagnostic
		}
		if d.IsDir() {
			// .terraform holds provider binaries and remote module copies;
			// .git holds history. Neither is the operator's own text, and the
			// former is where a walk would stop being cheap.
			if name := d.Name(); strings.HasPrefix(name, ".") && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".tf") && !strings.HasSuffix(name, ".tf.json") && !strings.HasSuffix(name, ".tofu") {
			return nil
		}
		if files >= stackFileCap {
			return fs.SkipAll
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // the operator's own configuration, read to warn them about it
		if readErr != nil {
			return nil //nolint:nilerr // same as above: skip what cannot be read
		}
		files++
		parts = append(parts, stripHCLComments(string(body)))
		return nil
	})
	return strings.Join(parts, "\n"), files
}

// stripHCLComments removes #, // and /* */ comments so a commented-out
// resource never triggers a warning — a warning that fires on dead text is a
// warning people learn to ignore, and teaching that is worse than staying
// quiet. Quoted strings are honoured: a "#" inside one is data, not a comment.
// TestAStackHazardInACommentStaysSilent fails without this.
func stripHCLComments(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	inString, inLineComment, inBlockComment := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
				out.WriteByte(c)
			}
		case inBlockComment:
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				inBlockComment = false
				i++
			} else if c == '\n' {
				out.WriteByte(c)
			}
		case inString:
			if c == '\\' && i+1 < len(s) {
				out.WriteByte(c)
				i++
				out.WriteByte(s[i])
				continue
			}
			if c == '"' {
				inString = false
			}
			out.WriteByte(c)
		default:
			switch {
			case c == '"':
				inString = true
				out.WriteByte(c)
			case c == '#':
				inLineComment = true
			case c == '/' && i+1 < len(s) && s[i+1] == '/':
				inLineComment = true
				i++
			case c == '/' && i+1 < len(s) && s[i+1] == '*':
				inBlockComment = true
				i++
			default:
				out.WriteByte(c)
			}
		}
	}
	return out.String()
}

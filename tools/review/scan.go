package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// finding is one thing the reviewer reads first. Line is 0 for a finding about
// a whole file. Every string in it that came from the pull request is escaped.
type finding struct {
	Rule    string `json:"rule"`
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// ---------------------------------------------------------------------------
// The sensitive-path list.

// patternShape is the closed alphabet a pattern may use. The list is matched
// by bash `[[ path == pattern ]]` in tools/ci/sensitive-paths.sh, where `*` and
// `?` also match `/`. A bracket, a backslash or an extglob would mean something
// this translation does not reproduce, so it is refused: an unrecognised shape
// must refuse, not pass, or a path the check catches would be missed here.
var patternShape = regexp.MustCompile(`^[A-Za-z0-9._/*?-]+$`)

type pattern struct {
	text string
	re   *regexp.Regexp
}

func loadPatterns(file string) ([]pattern, error) {
	f, err := os.Open(file) //nolint:gosec // the list of this checkout, named by the operator
	if err != nil {
		return nil, cannot("the sensitive-path list %s cannot be read (%v)", file, err)
	}
	defer func() { _ = f.Close() }()
	var ps []pattern
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !patternShape.MatchString(line) {
			return nil, cannot("%s:%d: pattern %q has a shape this program does not translate", file, n, escape(line))
		}
		var b strings.Builder
		b.WriteString("^")
		for _, r := range line {
			switch r {
			case '*':
				b.WriteString(".*")
			case '?':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		b.WriteString("$")
		ps = append(ps, pattern{text: line, re: regexp.MustCompile(b.String())})
	}
	if err := sc.Err(); err != nil {
		return nil, cannot("the sensitive-path list %s cannot be read (%v)", file, err)
	}
	if len(ps) == 0 {
		return nil, cannot("the sensitive-path list %s holds no pattern", file)
	}
	return ps, nil
}

func sensitive(p string, ps []pattern) (string, bool) {
	for _, pt := range ps {
		if pt.re.MatchString(p) {
			return pt.text, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// The patch.

type patchLine struct {
	kind byte // '+', '-', ' ', or '@' for a hunk header
	n    int  // line number in the new file, for '+' and ' '
	text string
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parsePatch reads the unified diff GitHub serves in a file's `patch`. A line
// it does not recognise is an error rather than a line skipped: a skipped line
// is an added line nobody scanned.
func parsePatch(patch string) ([]patchLine, error) {
	var out []patchLine
	next := -1
	for i, l := range strings.Split(strings.TrimSuffix(patch, "\n"), "\n") {
		if m := hunkHeader.FindStringSubmatch(l); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, cannot("patch line %d: hunk header %q", i+1, escape(l))
			}
			next = n
			out = append(out, patchLine{kind: '@'})
			continue
		}
		if next < 0 {
			return nil, cannot("patch line %d comes before any hunk header", i+1)
		}
		if l == "" { // an empty context line some diffs write without its space
			out = append(out, patchLine{kind: ' ', n: next})
			next++
			continue
		}
		switch l[0] {
		case '+':
			out = append(out, patchLine{kind: '+', n: next, text: l[1:]})
			next++
		case ' ':
			out = append(out, patchLine{kind: ' ', n: next, text: l[1:]})
			next++
		case '-':
			out = append(out, patchLine{kind: '-', text: l[1:]})
		case '\\': // "\ No newline at end of file"
		default:
			return nil, cannot("patch line %d has a shape this program does not know: %q", i+1, escape(truncate(l, 80)))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The rules. Each asks one question of one added line; review_test.go has a
// case per rule that passes every other rule and fails this one only.

type lineRule struct {
	id    string
	what  string
	files func(file string) bool // nil: every file
	match func(line string) (detail string, ok bool)
}

func ext(file string) string { return strings.ToLower(path.Ext(file)) }

func isGo(f string) bool    { return ext(f) == ".go" }
func isShell(f string) bool { e := ext(f); return e == ".sh" || e == ".bash" || e == ".zsh" }
func isYAML(f string) bool  { e := ext(f); return e == ".yml" || e == ".yaml" }
func isDoc(f string) bool   { e := ext(f); return e == ".md" || e == ".mdx" }
func isSpec(f string) bool {
	return strings.HasPrefix(f, "tools/falsify/specs/") && ext(f) == ".json"
}

func re(expr string) func(string) (string, bool) {
	rx := regexp.MustCompile(expr)
	return func(l string) (string, bool) {
		if m := rx.FindString(l); m != "" {
			return m, true
		}
		return "", false
	}
}

var (
	execCall = regexp.MustCompile(`\bos/exec\b|\bexec\.Command(?:Context)?\b|\bsyscall\.(?:Exec|ForkExec)\b|\bos\.StartProcess\b|\bsubprocess\b|\bos\.(?:system|popen|exec[lv]p?e?)\b|\beval\b`)
	pipeToSh = regexp.MustCompile(`\b(?:curl|wget)\b[^|]*\|\s*(?:sudo\s+)?(?:ba|z|da|k)?sh\b`)
	netCall  = regexp.MustCompile(`"net/http"|\bhttp\.(?:Get|Post|Head|PostForm|NewRequest(?:WithContext)?)\(|\bhttp\.DefaultClient\b|\bnet\.Dial\w*\(|\bDial(?:Context|TLS|Timeout)?\(|\burllib\b|\brequests\.(?:get|post|put|patch|delete|head|request|Session)\b|\bsocket\.`)
	netTool  = regexp.MustCompile(`\b(?:curl|wget)\s`)
	urlRe    = regexp.MustCompile("https?://[^\\s\"'<>()\\[\\]{}`]+")
	setenv   = regexp.MustCompile(`\bSetenv\(`)
	tSetenv  = regexp.MustCompile(`\bt\.Setenv\(`)
	envRead  = regexp.MustCompile(`\bos\.(?:Getenv|LookupEnv|Environ|Unsetenv|Clearenv|environ|getenv|putenv)\b|\bsyscall\.Getenv\b`)
	initFunc = regexp.MustCompile(`^\s*func\s+init\s*\(\s*\)`)
	encoded  = regexp.MustCompile(`[A-Za-z0-9+/_=-]{61,}`)
	usesRe   = regexp.MustCompile(`^\s*-?\s*uses:\s*(\S+)`)
)

var lineRules = []lineRule{
	{id: "exec", what: "process execution", match: func(l string) (string, bool) {
		if m := execCall.FindString(l); m != "" {
			return m, true
		}
		if m := pipeToSh.FindString(l); m != "" {
			return "download piped to a shell", true
		}
		return "", false
	}},
	{id: "exec", what: "backtick command substitution", files: isShell, match: func(l string) (string, bool) {
		if strings.Contains(l, "`") {
			return "backtick", true
		}
		return "", false
	}},
	{id: "network", what: "network access", match: re(netCall.String())},
	{id: "network", what: "a download tool", files: func(f string) bool { return !isDoc(f) }, match: re(netTool.String())},
	{id: "env", what: "the process environment", match: func(l string) (string, bool) {
		if m := envRead.FindString(l); m != "" {
			return m, true
		}
		// t.Setenv towards a t.TempDir() is how a test isolates itself.
		exempt := tSetenv.MatchString(l) && strings.Contains(l, "TempDir()")
		if setenv.MatchString(l) && !exempt {
			return "Setenv", true
		}
		return "", false
	}},
	{id: "init", what: "an init function, which runs before anything calls it", files: isGo, match: re(initFunc.String())},
	{id: "directive", what: "a compiler directive", files: isGo, match: re(`//go:(?:generate|linkname)\b|^\s*#cgo\b|^\s*import\s+"C"`)},
	{id: "unsafe", what: "package unsafe", files: isGo, match: re(`"unsafe"|\bunsafe\.`)},
	{id: "encoded", what: "a long base64 or hex string", match: func(l string) (string, bool) {
		for _, m := range encoded.FindAllString(l, -1) {
			if strings.ContainsAny(m, "0123456789") && strings.IndexFunc(m, unicode.IsLetter) >= 0 {
				return strconv.Itoa(len(m)) + " characters", true
			}
		}
		return "", false
	}},
	{id: "falsify-env", what: "a falsify spec that sets the environment of go test", files: isSpec, match: re(`"env"\s*:`)},
	{id: "control", what: "a control or invisible character in an added line", match: func(l string) (string, bool) {
		if r, ok := hidden(strings.TrimSuffix(l, "\r"), true); ok {
			return escape(string(r)), true
		}
		return "", false
	}},
}

// hidden reports the first rune a terminal or an editor would not show as
// itself: C0 and C1 controls, DEL, the bidirectional overrides of "Trojan
// Source", zero-width characters, or a byte that is not UTF-8.
func hidden(s string, allowTab bool) (rune, bool) {
	for i, r := range s {
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				return r, true
			}
		}
		if r == '\t' && allowTab {
			continue
		}
		if invisible(r) {
			return r, true
		}
	}
	return 0, false
}

// escapedCategories are the Unicode general categories whose characters a
// reader does not see as themselves: controls (Cc), format characters (Cf:
// the bidirectional marks and overrides of "Trojan Source" CVE-2021-42574,
// U+061C included, zero-width characters, the soft hyphen, the tag characters
// U+E0000-U+E007F that an agent reads and a human does not), the line and
// paragraph separators (Zl, Zp), surrogates (Cs) and private use (Co).
//
// It is the list tools/review/sandbox/escape.py names ESCAPED_CATEGORIES, so
// that the report and the sandbox's output hide nothing from each other.
// TestBothEscapersEscapeTheSameCategories fails when the two lists differ.
// Until 2026-11-16 this predicate was a hand-written list of ranges, and the
// security review replayed U+E0041, U+2028, U+2029, U+061C, U+00AD, U+FFF9
// and U+180E through it unescaped; TestEveryInvisibleCharacterIsEscaped
// holds one case for each.
var escapedCategories = []string{"Cc", "Cf", "Zl", "Zp", "Cs", "Co"}

var escapedTables = func() []*unicode.RangeTable {
	ts := make([]*unicode.RangeTable, 0, len(escapedCategories))
	for _, c := range escapedCategories {
		ts = append(ts, unicode.Categories[c])
	}
	return ts
}()

func invisible(r rune) bool {
	return unicode.In(r, escapedTables...)
}

// escape renders text from the pull request so that it cannot act on the
// terminal that prints it: every invisible rune and every byte that is not
// UTF-8 becomes a visible escape. Applied once, where the text enters the
// report, so both outputs carry the same safe string. A backslash is doubled,
// so that a path holding the text `\x1b` cannot pass for an escape.
func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\\':
			b.WriteString(`\\`)
		case invisible(r) && r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case invisible(r) && r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		case invisible(r): // \u with five digits would read as \uXXXX then a digit
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

func excerpt(l string) string { return truncate(escape(strings.TrimSpace(l)), 120) }

// ---------------------------------------------------------------------------
// The diff.

func scanDiff(files []prFile, ps []pattern, head, base map[string]string) ([]finding, error) {
	var out []finding
	for _, f := range files {
		name := escape(f.Filename)
		for _, p := range []string{f.Filename, f.PreviousFilename} {
			if p == "" {
				continue
			}
			if _, bad := hidden(p, false); bad {
				out = append(out, finding{Rule: "filename", File: name, Detail: "the path holds a control or invisible character: " + escape(p)})
			}
			if pt, ok := sensitive(p, ps); ok {
				out = append(out, finding{Rule: "sensitive", File: escape(p), Detail: "matches " + pt})
			}
		}
		base1 := path.Base(f.Filename)
		if base1 == "go.mod" || base1 == "go.sum" {
			out = append(out, finding{Rule: "go.mod", File: name, Detail: "changes the modules that are built"})
		}
		if f.Status != "removed" {
			if fd, ok := modeFinding(f, head, base); ok {
				out = append(out, fd)
			}
		}
		if f.Patch == nil {
			if f.Status != "removed" && (f.Changes > 0 || (f.Status != "renamed" && f.Status != "changed" && f.Status != "copied")) {
				out = append(out, finding{Rule: "unread", File: name, Detail: "the API serves no diff for it (binary or too large): read it by hand"})
			}
			continue
		}
		lines, err := parsePatch(*f.Patch)
		if err != nil {
			return nil, cannot("the diff of %s: %s", name, err.Error())
		}
		removed := map[string]bool{}
		for _, l := range lines {
			if l.kind == '-' {
				removed[strings.TrimSpace(l.text)] = true
			}
		}
		removedText := ""
		for k := range removed {
			removedText += k + "\n"
		}
		for _, l := range lines {
			if l.kind != '+' {
				continue
			}
			for _, rule := range lineRules {
				if rule.files != nil && !rule.files(f.Filename) {
					continue
				}
				if d, ok := rule.match(l.text); ok {
					out = append(out, finding{Rule: rule.id, File: name, Line: l.n, Excerpt: excerpt(l.text), Detail: rule.what + ": " + escape(d)})
				}
			}
			// A URL or an action is new when the removed lines of the same
			// file do not already hold it: a line edited around an existing
			// link is not a new host.
			for _, u := range urlRe.FindAllString(l.text, -1) {
				if strings.Contains(removedText, u) {
					continue
				}
				out = append(out, finding{Rule: "url", File: name, Line: l.n, Excerpt: excerpt(l.text), Detail: "new URL, host " + escape(host(u))})
			}
			if isYAML(f.Filename) {
				if m := usesRe.FindStringSubmatch(l.text); m != nil && !removed[strings.TrimSpace(l.text)] {
					out = append(out, finding{Rule: "action", File: name, Line: l.n, Excerpt: excerpt(l.text), Detail: "a new action reference: " + escape(m[1])})
				}
			}
		}
	}
	return out, nil
}

func host(u string) string {
	rest := u[strings.Index(u, "://")+3:]
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndexByte(rest, '@'); i >= 0 { // user:password@host
		rest = rest[i+1:]
	}
	return rest
}

// modeFinding reports a file that becomes executable, a symbolic link or a
// submodule. The files endpoint does not carry modes, so they come from the
// trees of the head and the base commits.
func modeFinding(f prFile, head, base map[string]string) (finding, bool) {
	now, ok := head[f.Filename]
	if !ok {
		return finding{}, false
	}
	before := base[f.Filename]
	if f.PreviousFilename != "" {
		before = base[f.PreviousFilename]
	}
	if now == before {
		return finding{}, false
	}
	what := map[string]string{
		"100755": "becomes an executable file",
		"120000": "becomes a symbolic link",
		"160000": "becomes a submodule",
	}[now]
	if what == "" {
		return finding{}, false
	}
	return finding{Rule: "mode", File: escape(f.Filename), Detail: what + " (mode " + now + ")"}, true
}

type fileDesc struct {
	Path      string `json:"path"`
	Previous  string `json:"previous_path,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Mode      string `json:"mode,omitempty"`
}

func describeFiles(files []prFile, head map[string]string) []fileDesc {
	out := make([]fileDesc, 0, len(files))
	for _, f := range files {
		out = append(out, fileDesc{
			Path: escape(f.Filename), Previous: escape(f.PreviousFilename), Status: escape(f.Status),
			Additions: f.Additions, Deletions: f.Deletions, Mode: head[f.Filename],
		})
	}
	return out
}

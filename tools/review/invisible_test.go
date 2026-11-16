package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The characters the security review of 2026-11-16 replayed through the
// first, hand-written predicate, each of which came out unescaped, plus one
// per category the predicate must cover. Every case is written as an escape in
// this source file: a raw invisible character here would be the defect itself.
//
// Each case asks the three places that use the predicate: the escaper (the
// character never reaches the output, and its spelling is unambiguous), the
// `filename` rule and the `control` rule. The mutations of
// tools/falsify/specs/an-outside-pull-request-is-read-before-it-runs.json that
// drop one category each name the case of that category.
func TestEveryInvisibleCharacterIsEscaped(t *testing.T) {
	ps, err := loadPatterns("../ci/sensitive-paths.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		r       rune
		spelled string
	}{
		{"uE0041-tag-character", '\U000E0041', `\U000e0041`},
		{"u2028-line-separator", '\u2028', `\u2028`},
		{"u2029-paragraph-separator", '\u2029', `\u2029`},
		{"u061C-arabic-letter-mark", '\u061c', `\u061c`},
		{"u00AD-soft-hyphen", '\u00ad', `\xad`},
		{"uFFF9-interlinear-annotation", '\ufff9', `\ufff9`},
		{"u180E-mongolian-vowel-separator", '\u180e', `\u180e`},
		{"u202E-right-to-left-override", '\u202e', `\u202e`},
		{"u001B-escape", '\x1b', `\x1b`},
		{"u0085-next-line", '\u0085', `\x85`},
		{"uE000-private-use", '\ue000', `\ue000`},
		{"uF0000-supplementary-private-use", '\U000F0000', `\U000f0000`},
	} {
		t.Run(c.name, func(t *testing.T) {
			text := "a" + string(c.r) + "b"
			if got := escape(text); got != "a"+c.spelled+"b" {
				t.Errorf("escape(%q) = %q, want %q", text, got, "a"+c.spelled+"b")
			}
			p := "@@ -1,0 +1,1 @@\n+x := 1 // " + text
			fs, err := scanDiff([]prFile{{Filename: "docs/" + text + ".go", Status: "modified", Changes: 1, Patch: &p}}, ps, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(rules(fs), ","); got != "control,filename" {
				t.Errorf("rules %s, want control,filename: the path and the added line both hide %U", got, c.r)
			}
			for _, f := range fs {
				if strings.ContainsRune(f.File+f.Excerpt+f.Detail, c.r) {
					t.Errorf("finding %+v carries %U raw", f, c.r)
				}
			}
		})
	}
}

// The accepting half: a character a reader sees as itself is printed as
// itself, whatever its script.
func TestAVisibleCharacterIsLeftAlone(t *testing.T) {
	for _, s := range []string{"caf\u00e9", "\u65e5\u672c", "\U0001F600", "\u00a0", "a b", "\u0627\u0644"} {
		if got := escape(s); got != s {
			t.Errorf("escape(%q) = %q: a visible character was escaped", s, got)
		}
		if r, bad := hidden(s, false); bad {
			t.Errorf("hidden(%q) reports %U", s, r)
		}
	}
}

// The sandbox (tools/review/sandbox/escape.py) and this report escape the same
// categories, so neither shows what the other hides. The list is read out of
// the Python source: a rename there that this test cannot find is a failure,
// not a pass.
func TestBothEscapersEscapeTheSameCategories(t *testing.T) {
	src, err := os.ReadFile("sandbox/escape.py")
	if err != nil {
		t.Fatalf("the sandbox's escaper cannot be read: %v", err)
	}
	m := regexp.MustCompile(`(?m)^ESCAPED_CATEGORIES = \(([^)]*)\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("sandbox/escape.py no longer declares ESCAPED_CATEGORIES = (...): this test cannot compare the two escapers")
	}
	py := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([A-Za-z]{2})"`).FindAllSubmatch(m[1], -1) {
		py[string(q[1])] = true
	}
	if len(py) == 0 {
		t.Fatalf("ESCAPED_CATEGORIES holds no category: %s", m[1])
	}
	gocats := map[string]bool{}
	for _, c := range escapedCategories {
		gocats[c] = true
	}
	for c := range py {
		if !gocats[c] {
			t.Errorf("escape.py escapes category %s and the report does not", c)
		}
	}
	for c := range gocats {
		if !py[c] {
			t.Errorf("the report escapes category %s and escape.py does not", c)
		}
	}
}

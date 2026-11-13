package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The banner says "the N sections of docs/limits.md each say what one costs".
// #872 added a generated product-coverage table under its own `## ` heading, so
// a count of headings went from 62 to 63 while one of the 63 was a table and
// not a cost. The number stayed exact; the sentence around it stopped being
// true, in a generated block nobody edits by hand.
//
// This is the accepting half and the refusing half in one table: a hand-written
// section counts, a section carrying a generated block does not, and the page
// as committed must not count the index.
func TestTheBannerDoesNotCountAGeneratedSectionAsALimit(t *testing.T) {
	for _, c := range []struct {
		name  string
		page  string
		count int
	}{
		{
			name:  "a hand-written section counts",
			page:  "# Limits\n\n## A limit\n\nit costs this\n\n## Another\n\nand this\n",
			count: 2,
		},
		{
			name: "a section carrying a generated block does not",
			page: "# Limits\n\n## Product coverage at a glance\n\n" +
				"<!-- limits-index:start -->\ntable\n<!-- limits-index:end -->\n\n" +
				"## A limit\n\nit costs this\n",
			count: 1,
		},
		{
			name: "the generated section may be last",
			page: "# Limits\n\n## A limit\n\nit costs this\n\n## Index\n\n" +
				"<!-- limits-index:start -->\ntable\n<!-- limits-index:end -->\n",
			count: 1,
		},
		{
			// A real limit may end with a generated table. Excluding every
			// section that merely contains a block counted 61 where the page
			// states 62: `feint start` detaches on Linux, and not on macOS is
			// measured prose that ends with the contracts table.
			name: "prose plus a generated block is still a limit",
			page: "# Limits\n\n## A measured limit\n\nit costs this\n\n" +
				"<!-- contracts:start -->\ntable\n<!-- contracts:end -->\n\n" +
				"## Index\n\n<!-- limits-index:start -->\ntable\n<!-- limits-index:end -->\n",
			count: 1,
		},
		{
			name:  "no sections at all",
			page:  "# Limits\n\nintro only\n",
			count: 0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := countLimitSections(c.page); got != c.count {
				t.Errorf("counted %d, want %d", got, c.count)
			}
		})
	}

	// And on the page as committed, which is the one the banner describes.
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "limits.md"))
	if err != nil {
		t.Fatalf("read the limits page: %v", err)
	}
	page := string(body)
	headings := strings.Count(page, "\n## ")
	limits := countLimitSections(page)
	if !strings.Contains(page, "<!-- limits-index:start -->") {
		t.Skip("docs/limits.md carries no generated section, so there is nothing to exclude")
	}
	if limits >= headings {
		t.Errorf("the page carries a generated section but the count still equals its headings "+
			"(%d headings, %d limits); the banner would again describe a table as a cost",
			headings, limits)
	}
}

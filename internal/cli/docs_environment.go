package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stephrobert/feint/internal/environment"
)

// The field reference for feint.yaml, rendered from the schema itself (#189).
//
// Written by hand it would be a second description of the same table, and this
// repository has measured twice what those cost: a page and a schema drift, the
// page reads as the promise, and the difference surfaces as a field somebody
// declared that nothing reads. So the sentence a field carries lives on the
// field, `feint docs` renders it, and `feint docs --check` fails when the page
// is behind — the same rail every other generated block rides.
//
// TestTheEnvironmentReferenceIsGeneratedFromTheSchema fails when a field is
// added to the schema and the page is not regenerated.

const (
	environmentStartMarker = "<!-- environment:start -->"
	environmentEndMarker   = "<!-- environment:end -->"
	// environmentDoc is where the reference lives. Named here rather than
	// taken as a flag: the page belongs to this repository, and a flag would
	// invite a second copy of it somewhere else.
	environmentDoc = "docs/environment.md"
)

// renderEnvironmentReference writes the two tables: what the file takes, and
// what it deliberately does not carry. The second is not a courtesy — a reader
// who writes `expose_to_network:` has a model of what this file is for, and a
// page that only lists the accepted fields leaves that model intact.
func renderEnvironmentReference() string {
	var b strings.Builder
	b.WriteString(docsGenerated + "\n\n")
	b.WriteString("| field | takes | default | read by | what it says |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, fd := range environment.Fields() {
		read := "nothing yet"
		if len(fd.ReadBy) > 0 {
			verbs := make([]string, 0, len(fd.ReadBy))
			for _, verb := range fd.ReadBy {
				verbs = append(verbs, "`feint "+verb+"`")
			}
			read = strings.Join(verbs, ", ")
		}
		def := "—"
		if fd.Default != "" {
			def = "`" + fd.Default + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n",
			fd.Path, fd.Takes, def, read, cell(fd.Doc))
	}

	b.WriteString("\n### What this file deliberately does not carry\n\n")
	b.WriteString("| field | why not |\n")
	b.WriteString("|---|---|\n")
	for _, fd := range environment.NotCarried() {
		fmt.Fprintf(&b, "| `%s` | %s |\n", fd.Path, cell(fd.Doc))
	}

	b.WriteString("\n### The ready conditions\n\n")
	for _, form := range environment.ConditionKinds {
		fmt.Fprintf(&b, "- `%s`\n", form)
	}
	b.WriteString("\nA condition that is not one of those forms is refused at load, with the list.\n")
	return b.String()
}

// cell folds a sentence onto one line of a Markdown table. A pipe inside it
// would end the cell, so it is escaped rather than dropped: a sentence that
// loses half of itself to the renderer reads as a sentence somebody wrote badly.
func cell(doc string) string {
	one := strings.Join(strings.Fields(doc), " ")
	return strings.ReplaceAll(one, "|", `\|`)
}

// spliceEnvironment answers whether the reference page is behind the schema.
// Absent page: no complaint, on the same terms as every other optional
// document — a user who installed the binary has no docs/ directory.
func spliceEnvironment(root string) (bool, error) {
	path := filepath.Join(root, environmentDoc)
	current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !strings.Contains(string(current), environmentStartMarker) {
		return false, nil
	}
	updated, err := spliceSection(string(current), environmentStartMarker, environmentEndMarker,
		renderEnvironmentReference())
	if err != nil {
		return false, err
	}
	return updated != string(current), nil
}

// writeSplicedEnvironment writes it.
func writeSplicedEnvironment(root string) error {
	path := filepath.Join(root, environmentDoc)
	current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(current), environmentStartMarker) {
		return nil
	}
	updated, err := spliceSection(string(current), environmentStartMarker, environmentEndMarker,
		renderEnvironmentReference())
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

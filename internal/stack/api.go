package stack

import "strings"

// File is one configuration file: the name to report it under, and its content.
type File struct {
	Name   string
	Source string
}

// Read folds the given files, in the order given, into one module.
//
// The order matters because Terraform merges override files last, so the caller
// decides it (see the loader) rather than this function sorting anything.
//
// A file the lexer cannot finish is recorded in Unreadable and contributes
// NOTHING: not a declaration, not a line of Stripped. That is deliberate in
// both directions. Its declarations are unknown, so claiming them would be
// worse than missing them; and feeding its text to a substring scan is exactly
// how the mechanism this replaces came to warn about commented-out code. The
// caller's verdict for a module with an unreadable file is `unknown`, which is
// the only answer available.
func Read(dir string, files []File) Module {
	mod := Module{Dir: dir}
	var stripped strings.Builder
	for _, f := range files {
		s, err := scan(f.Source)
		if err != nil {
			mod.Unreadable = append(mod.Unreadable, FileProblem{File: f.Name, Reason: err.Error()})
			continue
		}
		mod.Files = append(mod.Files, f.Name)
		readFile(&mod, f.Name, s.tokens)
		stripped.WriteString(s.stripped)
		stripped.WriteString("\n")
	}
	mod.Stripped = stripped.String()
	return mod
}

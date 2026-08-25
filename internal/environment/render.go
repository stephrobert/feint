package environment

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Render writes the declaration back out, in the schema's own order.
//
// It exists for the round trip #189 asks for — a valid file loads and renders —
// and that round trip is a test rather than a promise: a field added to the
// struct and forgotten here makes TestAValidDeclarationSurvivesARoundTrip go
// red, because the rendered copy no longer carries it.
//
// Only what the file declared is written. Rendering the defaults too would make
// every round trip grow the document, and a reader diffing their own file
// against the rendered one would be shown decisions they never made.
func Render(f *File) string {
	var b strings.Builder
	b.WriteString("version: " + strconv.Itoa(f.Version) + "\n")

	block := func(name string, body func(*strings.Builder)) {
		var inner strings.Builder
		body(&inner)
		if inner.Len() == 0 {
			return
		}
		b.WriteString("\n" + name + ":\n")
		b.WriteString(inner.String())
	}
	scalar := func(w *strings.Builder, path, key, value string) {
		if !f.declared[path] {
			return
		}
		fmt.Fprintf(w, "  %s: %s\n", key, quote(value))
	}

	block("cloud", func(w *strings.Builder) {
		scalar(w, "cloud.provider", "provider", f.Cloud.Provider)
	})
	block("emulator", func(w *strings.Builder) {
		scalar(w, "emulator.addr", "addr", f.Emulator.Addr)
		scalar(w, "emulator.state", "state", f.Emulator.State)
		scalar(w, "emulator.contracts", "contracts", f.Emulator.Contracts)
		scalar(w, "emulator.log_level", "log_level", f.Emulator.LogLevel)
		if f.declared["emulator.cleanup"] {
			fmt.Fprintf(w, "  cleanup: %t\n", f.Emulator.Cleanup)
		}
		renderMap(w, f.declared["emulator.env"], "env", f.Emulator.Env)
	})
	block("runtime", func(w *strings.Builder) {
		scalar(w, "runtime.mode", "mode", f.Runtime.Mode)
		renderList(w, f.declared["runtime.images"], "images", f.Runtime.Images)
	})
	block("snapshot", func(w *strings.Builder) {
		scalar(w, "snapshot.load", "load", f.Snapshot.Load)
	})
	block("iac", func(w *strings.Builder) {
		scalar(w, "iac.engine", "engine", f.IaC.Engine)
		scalar(w, "iac.directory", "directory", f.IaC.Directory)
		renderMap(w, f.declared["iac.vars"], "vars", f.IaC.Vars)
	})
	if f.declared["ready"] {
		b.WriteString("\nready:\n")
		for _, item := range f.Ready {
			b.WriteString("  - " + quote(item) + "\n")
		}
	}
	return b.String()
}

func renderMap(w *strings.Builder, declared bool, key string, values map[string]string) {
	if !declared {
		return
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	// Sorted, so two renders of one file are the same bytes and a diff means
	// something moved rather than that a map iterated differently.
	sort.Strings(names)
	fmt.Fprintf(w, "  %s:\n", key)
	for _, name := range names {
		fmt.Fprintf(w, "    %s: %s\n", name, quote(values[name]))
	}
}

func renderList(w *strings.Builder, declared bool, key string, values []string) {
	if !declared {
		return
	}
	fmt.Fprintf(w, "  %s:\n", key)
	for _, value := range values {
		fmt.Fprintf(w, "    - %s\n", quote(value))
	}
}

// quote wraps a value the reader would otherwise read as something else — a
// comment, an empty value, or a line whose leading space this parser trims.
// Everything else is written bare, because a document where every value carries
// quotes is a document nobody edits by hand.
func quote(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, "#:\"'") || strings.TrimSpace(value) != value {
		return strconv.Quote(value)
	}
	return value
}

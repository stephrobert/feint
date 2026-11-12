package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Engine is which of the two readers' loading rules apply.
//
// It is not decoration. The two disagree about which files a module is, and a
// verdict computed for the wrong one is a verdict about files the engine will
// not read. Measured 2026-11-12 with Terraform 1.16.1 and OpenTofu 1.11.4:
// OpenTofu reads *.tofu as well, and when main.tf and main.tofu both exist it
// reads main.tofu INSTEAD of main.tf; Terraform ignores *.tofu entirely.
type Engine string

const (
	EngineTerraform Engine = "terraform"
	EngineOpenTofu  Engine = "opentofu"
)

// moduleCap bounds the walk. A configuration is tens of modules; the cap is
// what keeps a preflight from reading a filesystem when a module source points
// somewhere surprising, and a walk that stops says so rather than reporting the
// modules it did not reach as empty.
const moduleCap = 64

// Load reads the module at dir and every module it calls that can be read with
// no network and no init of our own.
//
// # WHAT IS READ, WHICH IS NOT WHAT IS IN THE DIRECTORY
//
// A module is the top-level *.tf and *.tf.json of its own directory, and
// nothing below it. A nested directory no `module` block names is not part of
// the module: measured 2026-11-12, an aws_s3_bucket in sub/ did not appear in
// the plan. The mechanism this replaces walked recursively, so it reported text
// nobody would apply — the same noise its own top-level gate was written
// against, one level down.
//
// Override files are read last, which is the only thing that makes their merge
// possible: `override.tf` and `*_override.tf` (and their .json forms) replace
// what they name. Measured: an override.tf replaced a module's source, and
// .terraform/modules/modules.json recorded the override's value.
//
// Module calls are followed when the source is a literal local path, and
// otherwise when .terraform/modules/modules.json already holds a directory for
// them — that file is the engine's own resolution of every source, expression
// or not, and reading it costs nothing. What remains unresolved is named: a
// module nobody could open is a hole in the inventory, never an empty one.
func Load(dir string, engine Engine) Module {
	root := loadOne(dir, dir, "", engine)
	installed := readInstalledModules(dir)

	type pending struct {
		call   ModuleCall
		parent string
		path   string
	}
	queue := make([]pending, 0, len(root.ModuleCalls))
	for _, c := range root.ModuleCalls {
		queue = append(queue, pending{call: c, parent: dir, path: c.Name})
	}

	visited := map[string]bool{}
	if abs, err := filepath.Abs(dir); err == nil {
		visited[abs] = true
	}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		target, why := resolveModule(item.call, item.parent, dir, installed)
		if target == "" {
			root.markUnread(item.call.Name, why)
			continue
		}
		abs, err := filepath.Abs(target)
		if err != nil {
			root.markUnread(item.call.Name, "its directory could not be resolved")
			continue
		}
		if visited[abs] {
			// The same directory called twice is read once. Its declarations
			// are the same types whatever the call count, and the inventory is
			// a set of types.
			root.markRead(item.call.Name, target)
			continue
		}
		if len(visited) >= moduleCap {
			root.markUnread(item.call.Name, "the module walk stopped at its cap of 64 directories")
			continue
		}
		visited[abs] = true

		sub := loadOne(target, dir, item.path, engine)
		root.absorb(sub)
		root.markRead(item.call.Name, target)
		for _, c := range sub.ModuleCalls {
			queue = append(queue, pending{call: c, parent: target, path: item.path + "." + c.Name})
		}
	}

	root.Engine = engine
	return root
}

// resolveModule answers where a call's module is, or why it cannot be read.
func resolveModule(call ModuleCall, parentDir, rootDir string, installed map[string]string) (dir, why string) {
	if call.SourceLiteral && isLocal(call.Source) {
		return filepath.Join(parentDir, call.Source), ""
	}
	// The engine's own resolution, when an init has already written it down.
	if d, ok := installed[call.Key]; ok {
		return filepath.Join(rootDir, d), ""
	}
	if !call.SourceLiteral {
		return "", "its source is an expression, and this reader evaluates none"
	}
	return "", "source " + call.Source + " is not a local path, and no init has installed it here"
}

func isLocal(src string) bool {
	return strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../")
}

// readInstalledModules reads .terraform/modules/modules.json, which the engine
// writes at init with the directory it resolved every module to — Key, Source,
// Dir. Absent, the map is empty and only local paths are followed; nothing is
// claimed from its absence.
func readInstalledModules(dir string) map[string]string {
	body, err := os.ReadFile(filepath.Join(dir, ".terraform", "modules", "modules.json")) //nolint:gosec // the operator's own checkout
	if err != nil {
		return nil
	}
	var doc struct {
		Modules []struct {
			Key    string `json:"Key"`
			Source string `json:"Source"`
			Dir    string `json:"Dir"`
		} `json:"Modules"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, m := range doc.Modules {
		if m.Key != "" && m.Dir != "" {
			out[m.Key] = m.Dir
		}
	}
	return out
}

// loadOne reads one directory as one module.
//
// reportAs is the path the files are named under, so a declaration from a
// called module carries where it came from rather than a bare file name that
// exists three times over.
func loadOne(dir, rootDir, modulePath string, engine Engine) Module {
	primary, overrides, err := selectFiles(dir, engine)
	if err != nil {
		return Module{Dir: dir, Unreadable: []FileProblem{{
			File:   dir,
			Reason: "the directory could not be read: " + err.Error(),
		}}}
	}

	rel := func(name string) string {
		if p, err := filepath.Rel(rootDir, filepath.Join(dir, name)); err == nil {
			return p
		}
		return name
	}

	read := func(names []string) Module {
		var files []File
		for _, name := range names {
			body, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // the operator's own configuration
			if err != nil {
				continue
			}
			files = append(files, File{Name: rel(name), Source: string(body)})
		}
		return Read(dir, files)
	}

	mod := read(primary)
	if len(overrides) > 0 {
		mod.merge(read(overrides))
	}

	for i := range mod.Declarations {
		mod.Declarations[i].Module = modulePath
	}
	for i := range mod.ModuleCalls {
		mod.ModuleCalls[i].Key = joinKey(modulePath, mod.ModuleCalls[i].Name)
	}
	return mod
}

func joinKey(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// selectFiles is the engine's file set for one directory, in load order.
//
// Top level only: a module is its own directory's files, and a subdirectory no
// module block names is a different module or nothing at all.
func selectFiles(dir string, engine Engine) (primary, overrides []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	// OpenTofu's shadowing, measured: with main.tf and main.tofu both present,
	// only main.tofu is read. So the .tofu names are collected first and their
	// base names block the matching .tf.
	shadowed := map[string]bool{}
	if engine == EngineOpenTofu {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if base, ok := strings.CutSuffix(name, ".tofu"); ok {
				shadowed[base+".tf"] = true
			}
			if base, ok := strings.CutSuffix(name, ".tofu.json"); ok {
				shadowed[base+".tf.json"] = true
			}
		}
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isConfigFile(name, engine) || shadowed[name] {
			continue
		}
		if isOverride(name) {
			overrides = append(overrides, name)
			continue
		}
		primary = append(primary, name)
	}
	sort.Strings(primary)
	sort.Strings(overrides)
	return primary, overrides, nil
}

func isConfigFile(name string, engine Engine) bool {
	switch {
	case strings.HasSuffix(name, ".tf"), strings.HasSuffix(name, ".tf.json"):
		return true
	case strings.HasSuffix(name, ".tofu"), strings.HasSuffix(name, ".tofu.json"):
		// Terraform ignores these entirely, measured: a provider declared only
		// in a .tofu file did not appear in `terraform providers`.
		return engine == EngineOpenTofu
	}
	return false
}

// isOverride names the two forms the engine treats as overrides.
func isOverride(name string) bool {
	for _, ext := range []string{".tf", ".tf.json", ".tofu", ".tofu.json"} {
		base, ok := strings.CutSuffix(name, ext)
		if !ok {
			continue
		}
		if base == "override" || strings.HasSuffix(base, "_override") {
			return true
		}
	}
	return false
}

// merge folds an override module into this one.
//
// Bounded to what this reader claims to know, and the bound is the honest part:
// an override replaces what it names, so a module call's source is replaced
// when the override gives one. An override cannot introduce a resource the
// engine would accept — it must match an existing block — so nothing new is
// appended from it, and a declaration's TYPE is its label, which an override
// cannot change.
func (m *Module) merge(over Module) {
	m.Files = append(m.Files, over.Files...)
	m.Unreadable = append(m.Unreadable, over.Unreadable...)
	m.Stripped += over.Stripped

	for _, oc := range over.ModuleCalls {
		replaced := false
		for i := range m.ModuleCalls {
			if m.ModuleCalls[i].Name != oc.Name {
				continue
			}
			if oc.Source != "" || oc.SourceLiteral {
				m.ModuleCalls[i].Source = oc.Source
				m.ModuleCalls[i].SourceLiteral = oc.SourceLiteral
			}
			replaced = true
		}
		if !replaced {
			m.ModuleCalls = append(m.ModuleCalls, oc)
		}
	}
	for _, or := range over.Required {
		replaced := false
		for i := range m.Required {
			if m.Required[i].LocalName == or.LocalName {
				if or.Source != "" {
					m.Required[i].Source = or.Source
				}
				if or.Version != "" {
					m.Required[i].Version = or.Version
				}
				replaced = true
			}
		}
		if !replaced {
			m.Required = append(m.Required, or)
		}
	}
	if over.Backend != nil {
		m.Backend = over.Backend
	}
}

// absorb folds a called module's inventory into the root's.
func (m *Module) absorb(sub Module) {
	m.Files = append(m.Files, sub.Files...)
	m.Unreadable = append(m.Unreadable, sub.Unreadable...)
	m.Declarations = append(m.Declarations, sub.Declarations...)
	m.Required = append(m.Required, sub.Required...)
	m.ProviderBlocks = append(m.ProviderBlocks, sub.ProviderBlocks...)
	m.ModuleCalls = append(m.ModuleCalls, sub.ModuleCalls...)
	m.Stripped += sub.Stripped
}

func (m *Module) markRead(name, dir string) {
	for i := range m.ModuleCalls {
		if m.ModuleCalls[i].Name == name && m.ModuleCalls[i].ReadFrom == "" && m.ModuleCalls[i].Unread == "" {
			m.ModuleCalls[i].ReadFrom = dir
			return
		}
	}
}

func (m *Module) markUnread(name, why string) {
	for i := range m.ModuleCalls {
		if m.ModuleCalls[i].Name == name && m.ModuleCalls[i].ReadFrom == "" && m.ModuleCalls[i].Unread == "" {
			m.ModuleCalls[i].Unread = why
			return
		}
	}
}

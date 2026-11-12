package stack

import (
	"fmt"
	"sort"
)

// Mode is which of the two kinds of declaration a block is. They are kept apart
// because they are not the same claim: a resource is created and destroyed, a
// data source is only read, and a provider can serve one without the other.
type Mode string

const (
	ModeResource Mode = "resource"
	ModeData     Mode = "data"
)

// Declaration is one resource or data block, as declared.
//
// Type and Name are exact. Block labels are literal by the language's own rules,
// so no evaluation is involved and none is possible: Terraform 1.16.1 refuses
// `resource "${var.t}" "x"` with "Template sequences are not allowed in this
// string" (measured 2026-11-12).
type Declaration struct {
	Mode Mode
	Type string
	Name string
	File string
	Line int

	// Counted is true when the block carries `count` or `for_each`.
	//
	// It does NOT make the declaration unknown, and that distinction is the
	// whole point of the field. The type is declared whatever the count
	// evaluates to — with `count = 0` the provider is still required and the
	// resource is still in the plan's `configuration` (measured) — so what is
	// unknown is the number of instances, not the requirement. Reporting the
	// declaration itself as unknown would hide a known-unsupported type behind
	// `count = var.enabled`.
	Counted bool

	// Provider is the text of the `provider =` meta-argument when present.
	//
	// Kept verbatim and not resolved here, because resolving it is Terraform's
	// rule and belongs with the thing that knows the module tree. What it is
	// NOT is the type's prefix: measured 2026-11-12,
	// `scaleway_instance_server` with no required_providers resolves to
	// `hashicorp/scaleway`, and with `required_providers { scw = { source =
	// "scaleway/scaleway" } }` the same resource resolves to
	// `hashicorp/scaleway` BESIDE `scaleway/scaleway`. The prefix decides
	// nothing on its own.
	Provider string

	// Dynamic holds the labels of any `dynamic` blocks inside, which is how a
	// configuration generates nested blocks whose number is plan-time.
	Dynamic []string

	// Module is the call path this declaration was read under, empty for the
	// root. A type is a type wherever it is declared, and the path is what lets
	// a report say where to go and change it.
	Module string
}

// Address is the declaration's Terraform address, without the module path.
func (d Declaration) Address() string {
	if d.Mode == ModeData {
		return "data." + d.Type + "." + d.Name
	}
	return d.Type + "." + d.Name
}

// ProviderRequirement is one entry of `terraform { required_providers { … } }`.
type ProviderRequirement struct {
	LocalName string
	Source    string
	Version   string
	File      string
	Line      int
}

// ProviderBlock is a `provider "name" { … }` block and its alias, if any.
type ProviderBlock struct {
	Name  string
	Alias string
	File  string
	Line  int
}

// ModuleCall is a `module "name" { source = … }` block.
//
// SourceLiteral is false when the source was written as an expression, which is
// no longer exotic: Terraform 1.16.1 evaluates `source = local.src` and a
// `const = true` variable at init, and OpenTofu 1.11.4 evaluates a plain input
// variable (both measured 2026-11-12). This reader evaluates none of them and
// says so.
type ModuleCall struct {
	Name          string
	Source        string
	SourceLiteral bool
	File          string
	Line          int

	// Key is the address the engine files this call under — "services", or
	// "services.inner" for a nested one. It is the key of
	// .terraform/modules/modules.json, which is how a module an init already
	// installed can be read without one of our own.
	Key string

	// ReadFrom is the directory this call's module was read from, empty when
	// it was not read. Unread then says why, in words a reader can act on.
	//
	// Both are here rather than inferred by a caller, because "not local" and
	// "not read" are different facts and the first version of the report
	// confused them: a local path it simply did not follow counted as
	// resolved, and the verdict covered files nobody had opened.
	ReadFrom string
	Unread   string
}

// Backend is the state backend a module declares, if it declares one.
//
// Cloud marks the `cloud` block, which is the other way to say the same thing.
// Neither carries its attributes here: a backend is routinely completed on the
// command line with -backend-config, so the attributes in the file are not the
// configuration that will be used.
type Backend struct {
	Type  string
	Cloud bool
	File  string
	Line  int
}

// FileProblem is a file the reader would not claim to have read.
//
// It exists so that "unreadable" is a reported state rather than an empty
// result. A reader that returned no declarations for a file it could not
// tokenize would be indistinguishable from a file that declares nothing, and
// one of those two is a stack the preflight must call unknown.
type FileProblem struct {
	File   string
	Reason string
}

// Module is what one directory declares, read from the files the engine would
// load.
type Module struct {
	Dir    string
	Engine Engine
	Files  []string

	Unreadable []FileProblem

	Declarations   []Declaration
	Required       []ProviderRequirement
	ProviderBlocks []ProviderBlock
	ModuleCalls    []ModuleCall
	Backend        *Backend

	// Stripped is every file read, concatenated, with comments removed and
	// heredoc bodies kept.
	//
	// The bodies are kept on purpose: a startup script can carry a real cloud
	// hostname, and that is a true finding the hazard scan of internal/cli
	// looks for. What the lexer's heredoc state buys is not a shorter text, it
	// is that the body stops corrupting where the scanner thinks strings are.
	Stripped string
}

// Types is the set of declared types for one mode, sorted. This is the exact
// half of what the reader knows.
func (m Module) Types(mode Mode) []string {
	seen := map[string]bool{}
	for _, d := range m.Declarations {
		if d.Mode == mode && d.Type != "" {
			seen[d.Type] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// cursor walks one file's tokens. Newlines are kept in the stream because a
// block header ends at `{` and nothing else, but an attribute ends at the line,
// and telling `count` the attribute from `count` the identifier inside an
// expression needs that.
type cursor struct {
	toks []token
	i    int
	file string
	mod  *Module
}

func (c *cursor) done() bool { return c.i >= len(c.toks) }

func (c *cursor) peek() token {
	if c.done() {
		return token{kind: kindPunct}
	}
	return c.toks[c.i]
}

// next returns the next token that is not a newline, advancing past it.
func (c *cursor) next() (token, bool) {
	for !c.done() {
		t := c.toks[c.i]
		c.i++
		if t.kind != kindNewline {
			return t, true
		}
	}
	return token{}, false
}

func isPunct(t token, s string) bool { return t.kind == kindPunct && t.text == s }

// readFile folds one file's tokens into the module.
func readFile(mod *Module, file string, toks []token) {
	c := &cursor{toks: toks, file: file, mod: mod}
	for !c.done() {
		t, ok := c.next()
		if !ok {
			return
		}
		if t.kind != kindIdent {
			continue
		}
		// A block header: a type, then labels, then an opening brace. Anything
		// else at this level is an attribute or something this reader has no
		// business with, and is skipped.
		labels, opened := c.header()
		if !opened {
			continue
		}
		switch t.text {
		case "resource", "data":
			c.readDeclaration(t, labels)
		case "terraform":
			c.readTerraform()
		case "provider":
			c.readProvider(t, labels)
		case "module":
			c.readModule(t, labels)
		default:
			c.skipBlock()
		}
	}
}

// header reads the labels between a block type and its `{`.
//
// Both label forms are accepted, because the language accepts both: "quoted
// literal strings or naked identifiers" (hclsyntax spec), and Terraform 1.16.1
// takes `resource null_resource x {}` (measured). A label that is a template
// with an interpolation is returned as the empty string, which is how the
// caller reports a type it cannot know — though the engine refuses that file
// outright, so it should never be reachable from a configuration anyone can
// apply.
func (c *cursor) header() (labels []string, opened bool) {
	for {
		t, ok := c.next()
		if !ok {
			return labels, false
		}
		switch {
		case isPunct(t, "{"):
			return labels, true
		case t.kind == kindString:
			if t.literal {
				labels = append(labels, t.text)
			} else {
				labels = append(labels, "")
			}
		case t.kind == kindIdent:
			labels = append(labels, t.text)
		default:
			// An `=` here means this was an attribute, not a block header.
			return labels, false
		}
	}
}

// skipBlock consumes tokens to the brace that closes the block already opened.
func (c *cursor) skipBlock() {
	depth := 1
	for !c.done() {
		t, ok := c.next()
		if !ok {
			return
		}
		switch {
		case isPunct(t, "{"):
			depth++
		case isPunct(t, "}"):
			depth--
			if depth == 0 {
				return
			}
		}
	}
}

func (c *cursor) readDeclaration(head token, labels []string) {
	d := Declaration{Mode: Mode(head.text), File: c.file, Line: head.line}
	if len(labels) > 0 {
		d.Type = labels[0]
	}
	if len(labels) > 1 {
		d.Name = labels[1]
	}
	if d.Type == "" {
		c.mod.Unreadable = append(c.mod.Unreadable, FileProblem{
			File: c.file,
			Reason: fmt.Sprintf("line %d: a %s block's type is not a literal string, which the engine refuses; "+
				"nothing is claimed about it", head.line, head.text),
		})
	}

	depth := 1
	for !c.done() {
		t, ok := c.next()
		if !ok {
			break
		}
		switch {
		case isPunct(t, "{"):
			depth++
		case isPunct(t, "}"):
			depth--
			if depth == 0 {
				c.mod.Declarations = append(c.mod.Declarations, d)
				return
			}
		case depth == 1 && t.kind == kindIdent:
			switch t.text {
			case "count", "for_each":
				if isPunct(c.peekSignificant(), "=") {
					d.Counted = true
				}
			case "provider":
				if isPunct(c.peekSignificant(), "=") {
					d.Provider = c.attributeText()
				}
			case "dynamic":
				if label, ok := c.dynamicLabel(); ok {
					d.Dynamic = append(d.Dynamic, label)
					depth++
				}
			}
		}
	}
	c.mod.Declarations = append(c.mod.Declarations, d)
}

// peekSignificant is the next non-newline token without consuming it.
func (c *cursor) peekSignificant() token {
	for j := c.i; j < len(c.toks); j++ {
		if c.toks[j].kind != kindNewline {
			return c.toks[j]
		}
	}
	return token{kind: kindPunct}
}

// attributeText reads `= <tokens>` to the end of the line and returns the text
// verbatim, unevaluated. A traversal like `scaleway.secondary` comes back as
// written, which is what a report should print and is all a caller may rely on.
func (c *cursor) attributeText() string {
	// The `=` itself.
	if _, ok := c.next(); !ok {
		return ""
	}
	var parts []string
	for !c.done() {
		t := c.toks[c.i]
		if t.kind == kindNewline {
			break
		}
		c.i++
		switch t.kind {
		case kindString:
			parts = append(parts, `"`+t.text+`"`)
		case kindHeredoc:
			parts = append(parts, "<<"+t.text)
		default:
			parts = append(parts, t.text)
		}
	}
	return joinTight(parts)
}

// attributeString reads `= "literal"` and reports whether it was exactly that.
// Anything else — an expression, a heredoc, a reference — returns the text and
// false, so a caller can print what it saw and still call it unknown.
func (c *cursor) attributeString() (string, bool) {
	if !isPunct(c.peekSignificant(), "=") {
		return "", false
	}
	save := c.i
	if _, ok := c.next(); !ok {
		return "", false
	}
	t, ok := c.next()
	if !ok {
		return "", false
	}
	if t.kind == kindString && t.literal {
		if c.peek().kind == kindNewline || isPunct(c.peekSignificant(), "}") || isPunct(c.peekSignificant(), ",") {
			return t.text, true
		}
	}
	c.i = save
	return c.attributeText(), false
}

// dynamicLabel reads the label of a `dynamic "x" {` block and leaves the cursor
// just inside it.
func (c *cursor) dynamicLabel() (string, bool) {
	save := c.i
	t, ok := c.next()
	if !ok {
		return "", false
	}
	label := ""
	switch {
	case t.kind == kindString && t.literal:
		label = t.text
	case t.kind == kindIdent:
		label = t.text
	default:
		c.i = save
		return "", false
	}
	if open, ok := c.next(); ok && isPunct(open, "{") {
		return label, true
	}
	c.i = save
	return "", false
}

func (c *cursor) readProvider(head token, labels []string) {
	p := ProviderBlock{File: c.file, Line: head.line}
	if len(labels) > 0 {
		p.Name = labels[0]
	}
	depth := 1
	for !c.done() {
		t, ok := c.next()
		if !ok {
			break
		}
		switch {
		case isPunct(t, "{"):
			depth++
		case isPunct(t, "}"):
			depth--
			if depth == 0 {
				c.mod.ProviderBlocks = append(c.mod.ProviderBlocks, p)
				return
			}
		case depth == 1 && t.kind == kindIdent && t.text == "alias":
			if v, literal := c.attributeString(); literal {
				p.Alias = v
			}
		}
	}
	c.mod.ProviderBlocks = append(c.mod.ProviderBlocks, p)
}

func (c *cursor) readModule(head token, labels []string) {
	m := ModuleCall{File: c.file, Line: head.line}
	if len(labels) > 0 {
		m.Name = labels[0]
	}
	depth := 1
	for !c.done() {
		t, ok := c.next()
		if !ok {
			break
		}
		switch {
		case isPunct(t, "{"):
			depth++
		case isPunct(t, "}"):
			depth--
			if depth == 0 {
				c.mod.ModuleCalls = append(c.mod.ModuleCalls, m)
				return
			}
		case depth == 1 && t.kind == kindIdent && t.text == "source":
			v, literal := c.attributeString()
			m.Source, m.SourceLiteral = v, literal
		}
	}
	c.mod.ModuleCalls = append(c.mod.ModuleCalls, m)
}

// readTerraform reads the `terraform` block: required_providers, backend, cloud.
func (c *cursor) readTerraform() {
	depth := 1
	for !c.done() {
		t, ok := c.next()
		if !ok {
			return
		}
		switch {
		case isPunct(t, "{"):
			depth++
		case isPunct(t, "}"):
			depth--
			if depth == 0 {
				return
			}
		case depth == 1 && t.kind == kindIdent:
			switch t.text {
			case "required_providers":
				if open, ok := c.next(); ok && isPunct(open, "{") {
					c.readRequiredProviders(t.line)
				}
			case "backend":
				labels, opened := c.header()
				if opened {
					b := Backend{File: c.file, Line: t.line}
					if len(labels) > 0 {
						b.Type = labels[0]
					}
					c.mod.Backend = &b
					c.skipBlock()
				}
			case "cloud":
				if open, ok := c.next(); ok && isPunct(open, "{") {
					c.mod.Backend = &Backend{Cloud: true, File: c.file, Line: t.line}
					c.skipBlock()
				}
			}
		}
	}
}

// readRequiredProviders reads `local_name = { source = …, version = … }` entries.
//
// This is the one place the reader looks inside an object expression, and it is
// bounded on purpose: only `source` and `version`, only when they are literal
// strings. The entry is recorded either way, because a local name that exists
// with an unreadable source is still a local name, and dropping it would make
// a provider resolution silently fall back to `hashicorp/<name>`.
func (c *cursor) readRequiredProviders(line int) {
	depth := 1
	for !c.done() {
		t, ok := c.next()
		if !ok {
			return
		}
		switch {
		case isPunct(t, "}"):
			depth--
			if depth == 0 {
				return
			}
		case isPunct(t, "{"):
			depth++
		case depth == 1 && t.kind == kindIdent:
			req := ProviderRequirement{LocalName: t.text, File: c.file, Line: t.line}
			if !isPunct(c.peekSignificant(), "=") {
				continue
			}
			if _, ok := c.next(); !ok { // the `=`
				return
			}
			open, ok := c.next()
			if !ok {
				return
			}
			if !isPunct(open, "{") {
				// `name = "version-constraint"`, the legacy one-line form.
				if open.kind == kindString && open.literal {
					req.Version = open.text
				}
				c.mod.Required = append(c.mod.Required, req)
				continue
			}
			inner := 1
			for !c.done() && inner > 0 {
				it, ok := c.next()
				if !ok {
					break
				}
				switch {
				case isPunct(it, "{"):
					inner++
				case isPunct(it, "}"):
					inner--
				case inner == 1 && it.kind == kindIdent && it.text == "source":
					if v, literal := c.attributeString(); literal {
						req.Source = v
					}
				case inner == 1 && it.kind == kindIdent && it.text == "version":
					if v, literal := c.attributeString(); literal {
						req.Version = v
					}
				}
			}
			c.mod.Required = append(c.mod.Required, req)
		}
	}
	_ = line
}

func joinTight(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 && needsSpace(out[len(out)-1], p[0]) {
			out += " "
		}
		out += p
	}
	return out
}

// needsSpace keeps `a.b` tight and `== x` apart, so a printed expression reads
// the way the operator wrote it rather than as one run of characters.
func needsSpace(prev, next byte) bool {
	tight := func(b byte) bool { return b == '.' || b == '[' || b == ']' || b == '(' || b == ')' }
	if tight(prev) || tight(next) {
		return false
	}
	return isIdent(prev) == isIdent(next)
}

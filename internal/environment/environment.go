// Package environment reads feint.yaml: the declaration of one emulated
// environment, and the single source the `up` and `down` verbs act on.
//
// The boundary is the whole design (#189):
//
//	feint.yaml            how to bring the environment up   this project
//	Terraform / OpenTofu  what the infrastructure is        the user's repository
//	a snapshot            what the state currently is       an artefact
//
// The day this file grows a block describing a subnet, this project has started
// rewriting Terraform badly; the day it grows a package list, it has started
// rewriting Devbox badly. Both refusals are structural here rather than
// advisory: the schema is a closed table, and a key it does not name is refused
// by name at load.
//
// Two properties matter more than any field:
//
//   - **Nothing is accepted and then ignored.** A key this schema knows but no
//     verb reads yet is declared as such and said out loud at load (Fields
//     carries ReadBy, and an empty ReadBy is a warning, never silence). A file
//     that accepts everything and applies half of it is the exact lie this
//     project exists to avoid.
//   - **Nothing here describes what a provider can do.** The catalogue, the
//     image table and the login of a machine live in the packs. A second place
//     describing them would be a second place to keep in agreement, and this
//     repository has paid for that more than once.
package environment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultFile is the name read from the repository root when none is named.
const DefaultFile = "feint.yaml"

// Version is the only schema version this binary reads. A file naming another
// is refused with both numbers, the way a snapshot from the future already is.
const Version = 1

// File is one environment declaration, after loading and validation.
//
// Every field maps to a flag or a verb that exists. There is no field here that
// means something only this struct knows: that is the rule that keeps the file
// from becoming a second source for what the CLI already decides.
type File struct {
	// Path is where this declaration was read from, for messages that can be
	// acted on. Empty for a declaration parsed from bytes.
	Path string

	Version int

	Cloud struct {
		// Provider names the pack whose client environment `up` exports before
		// running the engine. Validated against the packs the binary mounts,
		// by the caller: which packs exist is not this package's knowledge.
		Provider string
	}

	Emulator struct {
		Addr      string
		State     string
		Contracts string
		LogLevel  string
		Cleanup   bool
		// Env is the environment the emulator's own process is started with.
		// FEINT_* only — see checkEnvName for why, and for the refusal.
		Env map[string]string
	}

	Runtime struct {
		// Mode is a --vm value. Absent means "off": starting machines is a side
		// effect on the operator's host, and it is asked for rather than
		// assumed.
		Mode string
		// Images are the machine images this environment needs present before
		// it starts. `up` checks them and refuses naming what is missing and
		// the command that builds it; it never builds them itself, because a
		// build takes minutes and is a side effect of its own.
		Images []string
	}

	Snapshot struct {
		// Load is the name of a snapshot loaded once the emulator answers, so a
		// fixture starts from a known state rather than from nothing.
		Load string
	}

	IaC struct {
		Engine    string
		Directory string
		// Vars are engine variables, exported as TF_VAR_<name>. The value
		// ${feint.endpoint} is substituted with the emulator's endpoint — the
		// one substitution this file has, so a stack that takes its endpoint
		// through a variable never carries the address twice.
		Vars map[string]string
	}

	// Ready are the conditions `up` waits for, each with a deadline and each
	// said out loud while it waits.
	Ready []string

	// declared records which known fields the file actually wrote, so the
	// loader can name the ones no verb reads yet.
	declared map[string]bool
}

// Engines are the infrastructure-as-code engines `up` runs. Both are the same
// language; which binary answers is the user's decision, not this project's.
var Engines = []string{"terraform", "opentofu"}

// RuntimeModes are the --vm values, and this list is the one the declaration is
// checked against.
//
// It is declared here rather than parsed out of the CLI because a schema that
// discovers its own vocabulary at run time cannot document it. What binds the
// two is a test, not a comment: TestEveryDeclaredRuntimeModeIsAModeTheBinaryTakes
// in internal/cli drives machineDriver with every name below and fails on one it
// rejects — and with a name that is not below, to prove it can tell the
// difference.
var RuntimeModes = []string{"off", "incus", "incus-vm", "incus-ovn", "auto"}

// LogLevels are the --log-level values.
var LogLevels = []string{"error", "warn", "info", "debug"}

// EndpointToken is the one substitution an iac.vars value may carry.
const EndpointToken = "${feint.endpoint}" //nolint:gosec // a substitution token, not a credential

// Load reads and validates a declaration from disk.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator's own declaration, named on the command line
	if err != nil {
		return nil, err
	}
	file, err := Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	file.Path = path
	return file, nil
}

// Parse reads and validates a declaration from its text.
func Parse(src string) (*File, error) {
	root, err := parseYAML(src)
	if err != nil {
		return nil, err
	}
	out := &File{declared: map[string]bool{}}
	out.Emulator.Addr = "127.0.0.1:4599"
	out.Emulator.LogLevel = "info"
	out.Runtime.Mode = "off"
	out.IaC.Directory = "."

	if err := walk(out, root, ""); err != nil {
		return nil, err
	}
	if out.Version == 0 {
		return nil, fmt.Errorf("no `version:` field; this binary reads version %d", Version)
	}
	if out.Version != Version {
		return nil, fmt.Errorf("version %d, and this binary reads version %d", out.Version, Version)
	}
	if err := out.check(); err != nil {
		return nil, err
	}
	return out, nil
}

// Declared answers whether the file itself wrote this field, as opposed to it
// holding the default. `up` uses it to say what it is honouring.
func (f *File) Declared(path string) bool { return f.declared[path] }

// Unread names the fields this file declares that no verb reads yet, so a load
// can say so out loud. A field declared and not read is acceptable only when it
// is named as such; one read halfway is not acceptable at all.
func (f *File) Unread() []string {
	var out []string
	for _, fd := range Fields() {
		if len(fd.ReadBy) == 0 && f.declared[fd.Path] {
			out = append(out, fd.Path)
		}
	}
	sort.Strings(out)
	return out
}

// Endpoint renders the emulator's address as a URL a client can use.
func (f *File) Endpoint() string {
	addr := f.Emulator.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr
}

// Dir answers the directory the declaration sits in, which is what every
// relative path in it is relative to. A declaration that resolved paths against
// the caller's working directory would mean the same file did two things
// depending on where it was run from, which is the property #192 exists to
// remove.
func (f *File) Dir() string {
	if f.Path == "" {
		return "."
	}
	return filepath.Dir(f.Path)
}

// Resolve makes one of the declaration's paths absolute against Dir.
func (f *File) Resolve(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(f.Dir(), path)
}

// EngineVars renders iac.vars with the endpoint substituted.
func (f *File) EngineVars() map[string]string {
	out := make(map[string]string, len(f.IaC.Vars))
	for name, value := range f.IaC.Vars {
		out[name] = strings.ReplaceAll(value, EndpointToken, f.Endpoint())
	}
	return out
}

// walk assigns every key of a block, refusing the ones this schema does not
// name. The refusal carries the reason when the key is one this project
// deliberately does not carry, and the list of siblings otherwise: "unknown
// key" alone tells a reader they made a mistake without telling them which.
func walk(out *File, block node, prefix string) error {
	for _, key := range block.order {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		child := block.mapping[key]
		if reason, refused := notCarried[path]; refused {
			return fmt.Errorf("line %d: `%s` is not carried by this file: %s", child.line, path, reason)
		}
		fd, known := fieldsByPath[path]
		if !known {
			if isBranch(path) {
				if child.kind != kindMapping {
					return fmt.Errorf("line %d: `%s` takes a block of fields, not %s", child.line, path, child.kind)
				}
				if err := walk(out, child, path); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("line %d: unknown field `%s`; %s", child.line, path, siblings(prefix))
		}
		if err := fd.assign(out, child); err != nil {
			return fmt.Errorf("line %d: `%s`: %w", child.line, path, err)
		}
		out.declared[path] = true
	}
	return nil
}

// siblings names what a block does take, for the refusal above.
func siblings(prefix string) string {
	var names []string
	for _, fd := range Fields() {
		parent, leaf := split(fd.Path)
		if parent == prefix {
			names = append(names, leaf)
		}
	}
	for path := range branches {
		parent, leaf := split(path)
		if parent == prefix {
			names = append(names, leaf)
		}
	}
	sort.Strings(names)
	if prefix == "" {
		return "this file takes: " + strings.Join(names, ", ")
	}
	return "`" + prefix + "` takes: " + strings.Join(names, ", ")
}

func split(path string) (parent, leaf string) {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return "", path
	}
	return path[:i], path[i+1:]
}

func isBranch(path string) bool { return branches[path] }

// branches are the blocks of the schema. Listed rather than derived from the
// field paths so that a typo in a block name — `runtimes:` for `runtime:` — is
// refused as an unknown field instead of walked into and reported one level
// deeper, where the reader has to work out which line was wrong.
var branches = map[string]bool{
	"cloud":    true,
	"emulator": true,
	"runtime":  true,
	"snapshot": true,
	"iac":      true,
}

// notCarried names what this file deliberately does not carry, with the reason.
//
// Refusing by name is the point. A reader who writes `expose_to_network: true`
// into a declaration has a model of what this file is for, and an "unknown
// field" answer leaves that model intact. These four answers correct it.
var notCarried = map[string]string{
	"emulator.coverage": "`--coverage` is a flag of `feint serve` alone, which `feint start` refuses and " +
		"`feint up` composes; run `feint coverage` against the running emulator instead",
	"emulator.shapes": "`--shapes` is a flag of `feint serve` alone, which `feint start` refuses and " +
		"`feint up` composes; run `feint shapes --check` against the running emulator instead",
	"emulator.expose_to_network": "putting an emulator that accepts every credential on the network is a " +
		"decision the person at the keyboard makes, never one a file they cloned makes for them; " +
		"type `feint serve --expose-to-network` and read SECURITY.md first",
	"proxy": "a recorder needs a real cloud endpoint and real credentials, which is the one thing this " +
		"file must never carry; run `feint proxy --upstream … --record …` beside the emulator",
}

// Field is one key of the schema, and the only description of it there is.
//
// The reference page is rendered from this table (`feint docs`), so a field
// added without a sentence is a field the page shows without one — visible
// rather than missing. ReadBy names the verbs that act on the field today; an
// empty ReadBy is a field declared and not yet read, which the loader says out
// loud.
type Field struct {
	Path    string
	Takes   string
	Default string
	Doc     string
	ReadBy  []string
	assign  func(*File, node) error
}

// Fields is the schema, in the order the reference presents it.
func Fields() []Field { return fields }

var fields = []Field{
	{
		Path: "version", Takes: "a number", Default: "",
		Doc:    "The schema version. Only " + strconv.Itoa(Version) + " is read; another is refused naming both.",
		ReadBy: []string{"up", "down"},
		assign: func(f *File, n node) error {
			v, err := scalarInt(n)
			if err != nil {
				return err
			}
			f.Version = v
			return nil
		},
	},
	{
		Path: "cloud.provider", Takes: "a provider name", Default: "",
		Doc: "The pack whose client environment `up` exports before running the engine — the same " +
			"variables `feint env <provider>` prints, including the endpoint form that provider's " +
			"clients want. Refused when the binary mounts no such pack.",
		ReadBy: []string{"up", "down"},
		assign: func(f *File, n node) error { return scalarInto(n, &f.Cloud.Provider) },
	},
	{
		Path: "emulator.addr", Takes: "a listen address", Default: "127.0.0.1:4599",
		Doc:    "Where the emulator listens: `serve --addr`.",
		ReadBy: []string{"up", "down"},
		assign: func(f *File, n node) error { return scalarInto(n, &f.Emulator.Addr) },
	},
	{
		Path: "emulator.state", Takes: "a path", Default: "",
		Doc: "The JSON file the store is loaded from and persisted to: `serve --state`. " +
			"Relative to the declaration's own directory.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error { return scalarInto(n, &f.Emulator.State) },
	},
	{
		Path: "emulator.contracts", Takes: "a directory", Default: "",
		Doc: "The API descriptions every response is checked against: `serve --contracts`. " +
			"Relative to the declaration's own directory.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error { return scalarInto(n, &f.Emulator.Contracts) },
	},
	{
		Path: "emulator.log_level", Takes: "error, warn, info or debug", Default: "info",
		Doc:    "`serve --log-level`, which is what `feint logs` then shows.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error { return scalarInto(n, &f.Emulator.LogLevel) },
	},
	{
		Path: "emulator.cleanup", Takes: "true or false", Default: "false",
		Doc:    "Remove the machines and networks the run created before exiting: `serve --cleanup`.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error { return scalarBool(n, &f.Emulator.Cleanup) },
	},
	{
		Path: "emulator.env", Takes: "a block of FEINT_* variables", Default: "",
		Doc: "The environment the emulator's own process starts with — FEINT_BOOT_IMAGES and the other " +
			"FEINT_* knobs, which are read server-side and so cannot be exported after the start. " +
			"FEINT_* only: a declaration that could set any variable of a process it spawns would be a " +
			"different kind of file. One name is refused outright: FEINT_EXOSCALE_ALLOW_TERRAFORM, " +
			"because #525 measured a stack declaration arming it for whatever provider the engine " +
			"resolved — an escape hatch that consequential is exported by hand, in the shell that runs " +
			"`feint serve`, never carried by a file that travels.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error {
			m, err := scalarMap(n, checkEnvName)
			if err != nil {
				return err
			}
			f.Emulator.Env = m
			return nil
		},
	},
	{
		Path: "runtime.mode", Takes: strings.Join(RuntimeModes, ", "), Default: "off",
		Doc: "What backs a powered-on server: the `--vm` values and nothing else. Absent means `off`, " +
			"because starting machines is a side effect this project asks for rather than assumes. " +
			"`up` checks the host can deliver the named mode before it starts anything, and refuses " +
			"rather than downgrading.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error {
			return scalarOneOf(n, RuntimeModes, &f.Runtime.Mode)
		},
	},
	{
		Path: "runtime.images", Takes: "a list of family/version", Default: "",
		Doc: "The machine images this environment needs present. `up` checks them and refuses naming " +
			"what is missing and the `feint images` command that builds it; it never builds them " +
			"itself, because a build takes minutes. Ignored when `runtime.mode` is `off`, where " +
			"nothing boots.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error {
			list, err := scalarList(n)
			if err != nil {
				return err
			}
			for _, name := range list {
				if strings.Count(name, "/") == 0 {
					return fmt.Errorf("%q is not a family/version image name", name)
				}
			}
			f.Runtime.Images = list
			return nil
		},
	},
	{
		Path: "snapshot.load", Takes: "a snapshot name", Default: "",
		Doc: "A snapshot loaded once the emulator answers, so this environment starts from a known " +
			"state rather than from nothing: `feint snapshot load <name>`. The name, never a path — " +
			"snapshots live where `feint snapshot list` says they live.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error { return scalarInto(n, &f.Snapshot.Load) },
	},
	{
		Path: "iac.engine", Takes: strings.Join(Engines, " or "), Default: "",
		Doc: "The engine `up` runs and `down` destroys with. Absent means this environment declares no " +
			"infrastructure, and `up` brings the control plane up and stops there.",
		ReadBy: []string{"up", "down"},
		assign: func(f *File, n node) error { return scalarOneOf(n, Engines, &f.IaC.Engine) },
	},
	{
		Path: "iac.directory", Takes: "a directory", Default: ".",
		Doc: "Where the engine runs, relative to the declaration's own directory. The engine runs " +
			"there in place — a copy of `*.tf` would leave a local module behind, which is one of the " +
			"four things this file exists to make impossible.",
		ReadBy: []string{"up", "down"},
		assign: func(f *File, n node) error { return scalarInto(n, &f.IaC.Directory) },
	},
	{
		Path: "iac.vars", Takes: "a block of variables", Default: "",
		Doc: "Engine variables, exported as `TF_VAR_<name>`. The value `" + EndpointToken + "` is " +
			"replaced by the emulator's endpoint, and it is the only substitution this file has: a " +
			"stack that takes its endpoint through a variable then carries the address once, here.",
		ReadBy: []string{"up", "down"},
		assign: func(f *File, n node) error {
			m, err := scalarMap(n, checkVarName)
			if err != nil {
				return err
			}
			f.IaC.Vars = m
			return nil
		},
	},
	{
		Path: "ready", Takes: "a list of conditions", Default: "",
		Doc: "What `up` waits for before it says the environment is up, each with a deadline and each " +
			"said out loud while it waits. Three forms: `http:<path>` (the emulator answers below " +
			"400), `tcp:<host>:<port>` (a connection is accepted), and `resource:<kind>[:<count>]` " +
			"(the emulator's own inventory holds at least that many). Every one is asserted against " +
			"the emulator, never against the engine's state file.",
		ReadBy: []string{"up"},
		assign: func(f *File, n node) error {
			list, err := scalarList(n)
			if err != nil {
				return err
			}
			for _, item := range list {
				if _, err := ParseCondition(item); err != nil {
					return err
				}
			}
			f.Ready = list
			return nil
		},
	},
}

// NotCarried answers what the file deliberately does not carry, with the
// reason, for the generated reference. Sorted, so the page is stable.
func NotCarried() []Field {
	out := make([]Field, 0, len(notCarried))
	for path, why := range notCarried {
		out = append(out, Field{Path: path, Doc: why})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

var fieldsByPath = func() map[string]Field {
	out := make(map[string]Field, len(fields))
	for _, fd := range fields {
		out[fd.Path] = fd
	}
	return out
}()

// check holds the cross-field rules, which are the ones a per-field assign
// cannot see.
func (f *File) check() error {
	if f.IaC.Engine == "" && (f.declared["iac.directory"] || f.declared["iac.vars"]) {
		return fmt.Errorf("`iac` declares a directory or variables and no `engine`; name one of %s",
			strings.Join(Engines, ", "))
	}
	if !contains(LogLevels, f.Emulator.LogLevel) {
		return fmt.Errorf("`emulator.log_level`: %q is not one of %s",
			f.Emulator.LogLevel, strings.Join(LogLevels, ", "))
	}
	if f.Snapshot.Load != "" && strings.ContainsAny(f.Snapshot.Load, "/\\") {
		return fmt.Errorf("`snapshot.load`: %q is a path, and this field takes a snapshot name; "+
			"`feint snapshot list` names them", f.Snapshot.Load)
	}
	return nil
}

// checkEnvName holds emulator.env to this project's own knobs.
//
// The field exists so that a FEINT_* knob — read inside the emulator's
// process, so exporting it after the start does nothing — travels with the
// declaration instead of in a chat paragraph. That need is met by FEINT_*
// alone, and a declaration able to set any variable of a process it spawns is
// a different and much larger thing. Refusing names both facts.
//
// One of this project's own names is refused too, and by name.
// examples/stacks/exoscale/feint.yaml used to carry
// FEINT_EXOSCALE_ALLOW_TERRAFORM: "1", and #525 measured what that arms: the
// declaration lifted the emulator's one Terraform refusal for whatever
// provider the engine resolved — the published one, that day — and five
// signed requests left for api-ch-*.exoscale.com. A declaration travels with
// a stack to machines and shells its author never sees, so an escape hatch
// that consequential is exported by hand, in the shell that runs
// `feint serve`, by someone who has read docs/limits.md — never by a file.
//
// TestAnEnvironmentVariableOutsideTheProjectsOwnIsRefused fails without the
// prefix check; TestTheDeclarationCannotArmTheExoscaleTerraformEscape fails
// without the name check.
func checkEnvName(name string) error {
	if !strings.HasPrefix(name, "FEINT_") {
		return fmt.Errorf("%q: this block sets the emulator's own knobs, so a name has to start with "+
			"FEINT_; export anything else in the shell that runs `feint up`", name)
	}
	if name == "FEINT_EXOSCALE_ALLOW_TERRAFORM" {
		return fmt.Errorf("%q: refused since #525, when a stack declaration arming it let the published "+
			"Exoscale provider split a run between the emulator and the real cloud. It is exported by "+
			"hand, in the shell that runs `feint serve`, or not at all — and `feint up` refuses "+
			"`iac.engine: terraform` for Exoscale regardless, until upstream "+
			"exoscale/terraform-provider-exoscale#573 is fixed", name)
	}
	for _, c := range name {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			return fmt.Errorf("%q: an environment variable name here is FEINT_ and then capitals, "+
				"digits and underscores", name)
		}
	}
	return nil
}

// checkVarName refuses a variable name Terraform could not take, which is the
// cheapest way to turn a typo into a refusal rather than into an engine
// variable nothing reads.
func checkVarName(name string) error {
	if name == "" {
		return errNamelessVariable
	}
	for i, c := range name {
		letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		digit := c >= '0' && c <= '9'
		if i == 0 && !letter {
			return fmt.Errorf("%q: an engine variable name starts with a letter or an underscore", name)
		}
		if !letter && !digit && c != '-' {
			return fmt.Errorf("%q: an engine variable name is letters, digits, dashes and underscores", name)
		}
	}
	return nil
}

// errNamelessVariable is the one refusal with no value to name.
var errNamelessVariable = errors.New("a variable with no name")

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func scalarInto(n node, dst *string) error {
	if n.kind != kindScalar {
		return fmt.Errorf("takes a value, not %s", n.kind)
	}
	if n.scalar == "" {
		return fmt.Errorf("is empty; give it a value or remove the line")
	}
	*dst = n.scalar
	return nil
}

func scalarInt(n node) (int, error) {
	if n.kind != kindScalar {
		return 0, fmt.Errorf("takes a number, not %s", n.kind)
	}
	v, err := strconv.Atoi(n.scalar)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", n.scalar)
	}
	return v, nil
}

func scalarBool(n node, dst *bool) error {
	if n.kind != kindScalar {
		return fmt.Errorf("takes true or false, not %s", n.kind)
	}
	switch n.scalar {
	case "true":
		*dst = true
	case "false":
		*dst = false
	default:
		return fmt.Errorf("%q is not true or false", n.scalar)
	}
	return nil
}

func scalarOneOf(n node, allowed []string, dst *string) error {
	if n.kind != kindScalar {
		return fmt.Errorf("takes one of %s, not %s", strings.Join(allowed, ", "), n.kind)
	}
	if !contains(allowed, n.scalar) {
		return fmt.Errorf("%q is not one of %s", n.scalar, strings.Join(allowed, ", "))
	}
	*dst = n.scalar
	return nil
}

func scalarList(n node) ([]string, error) {
	if n.kind != kindSeq {
		return nil, fmt.Errorf("takes a list of values, not %s", n.kind)
	}
	out := make([]string, 0, len(n.seq))
	for _, item := range n.seq {
		out = append(out, item.scalar)
	}
	return out, nil
}

func scalarMap(n node, checkName func(string) error) (map[string]string, error) {
	if n.kind != kindMapping {
		return nil, fmt.Errorf("takes a block of `name: value` lines, not %s", n.kind)
	}
	out := make(map[string]string, len(n.mapping))
	for _, name := range n.order {
		child := n.mapping[name]
		if child.kind != kindScalar {
			return nil, fmt.Errorf("%q takes a value, not %s", name, child.kind)
		}
		if err := checkName(name); err != nil {
			return nil, err
		}
		out[name] = child.scalar
	}
	return out, nil
}

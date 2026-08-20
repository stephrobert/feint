package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The verb with the most use per line of code.
//
// `eval "$(feint env scaleway)"` has to be enough to point scw, Terraform or an
// SDK at the emulator. Before it, the README asked a reader to copy seven
// environment variables by hand, and a typo in any of them produced an error
// that says nothing about the typo.
//
// Two details decide whether `eval` works at all, and both are about which
// stream carries what. **Only the export lines go to stdout.** Every comment,
// every hint, every warning goes to stderr — a single stray line of prose on
// stdout and the shell tries to execute it. And **--unset prints the removals**,
// because a developer who pointed a shell at the emulator needs a way back to
// the real cloud without opening a new terminal.
//
// Nothing here names a provider. The variable names are provider knowledge and
// live in the pack, behind Pack.Env; this file iterates whatever packs the
// server mounts. The usual test: could this line be written identically for
// another provider? Every line here can.

func envCommand(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("env")
	shell := fs.String("shell", "bash", "shell syntax: bash, fish or powershell")
	endpoint := fs.String("endpoint", "", "the emulator's address (default: http://<the --addr default>)")
	unset := fs.Bool("unset", false, "print the removals instead of the exports")
	client := fs.String("client", "", "client family the exports target, for a provider whose clients disagree about a value")

	// The provider comes first and is taken before parsing, because the standard
	// flag package stops at the first non-flag argument: with `env scaleway
	// --endpoint …`, Parse would leave three positional arguments and no
	// endpoint at all.
	//
	// That is not a cosmetic bug. It made this command print nothing on stdout,
	// an `eval` of nothing succeed silently, and the client that followed fall
	// back to the operator's stored credentials — which created a server and a
	// flexible IP on a real, paying account. A command that produces no output
	// and exits 0 is the shape of that accident, and the check below is what
	// makes it impossible.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "feint: env needs a provider first: feint env <provider> [flags]")
		return exitError
	}
	wanted := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "feint: unexpected argument %q; env takes one provider and flags\n", fs.Arg(0))
		return exitError
	}

	srv, _, err := newServer(nil)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}

	names := make([]string, 0, len(srv.Packs()))
	for _, p := range srv.Packs() {
		names = append(names, p.Name())
	}
	sort.Strings(names)

	var pack packEnv
	for _, p := range srv.Packs() {
		if p.Name() == wanted {
			pack = p
			break
		}
	}
	if pack == nil {
		fmt.Fprintf(stderr, "feint: no provider %q; this emulator serves %s\n", wanted, strings.Join(names, ", "))
		return exitError
	}

	target := *endpoint
	if target == "" {
		target = defaultEndpoint()
	}
	env := pack.Env(target)
	if *client != "" {
		// A client family only means something to a pack whose clients
		// disagree; anywhere else the flag would silently print the same thing
		// and teach that it does something. Refusing names both facts.
		chooser, ok := pack.(packEnvClients)
		if !ok {
			fmt.Fprintf(stderr, "feint: the %s pack serves every client the same environment; drop --client\n", wanted)
			return exitError
		}
		env, ok = chooser.EnvFor(target, *client)
		if !ok {
			fmt.Fprintf(stderr, "feint: the %s pack knows no client %q; it serves %s\n",
				wanted, *client, strings.Join(chooser.EnvClients(), ", "))
			return exitError
		}
	}

	render, ok := renderers[*shell]
	if !ok {
		fmt.Fprintf(stderr, "feint: unknown shell %q; bash, fish or powershell\n", *shell)
		return exitError
	}

	keys := make([]string, 0, len(env.Vars))
	for key := range env.Vars {
		keys = append(keys, key)
	}
	// Sorted, so two runs emit the same bytes and a diff of the output means
	// something changed rather than that a map iterated differently.
	sort.Strings(keys)

	// Empty output is a failure, never a quiet success.
	//
	// A pack that declares no variable would have this command print nothing and
	// exit 0, and the caller's `eval` would then leave the shell pointed at
	// whatever it was pointed at before — which is a real cloud, for anybody who
	// has ever logged in. Refusing is the only safe answer.
	if len(keys) == 0 {
		fmt.Fprintf(stderr, "feint: the %s pack declares no environment; refusing to print nothing, "+
			"because an eval of nothing leaves your shell pointed at the real cloud\n", wanted)
		return exitError
	}

	for _, key := range keys {
		if *unset {
			fmt.Fprintln(stdout, render.unset(key))
			continue
		}
		fmt.Fprintln(stdout, render.export(key, env.Vars[key]))
	}

	// stderr, always. This is what keeps `eval` safe while still telling the
	// user the thing they need to know.
	if !*unset && env.Note != "" {
		fmt.Fprintf(stderr, "note: %s\n", env.Note)
	}

	// The shell itself can carry a value that sends this provider's clients to
	// the real cloud no matter what was just printed — a profile variable the
	// client honours before it reads any of these exports. Only the pack knows
	// which names those are; only this command is present at the moment the
	// user can still act on them. Skipped with --unset, which is the deliberate
	// way back to the real cloud.
	//
	// TestEnvHazardWarningsReachStderrNeverStdout fails if a warning ever
	// reaches stdout, where eval would execute it.
	if !*unset {
		if hazards, ok := pack.(packEnvHazards); ok {
			for _, warning := range hazards.EnvHazards(os.LookupEnv) {
				fmt.Fprintf(stderr, "warning: %s\n", warning)
			}
		}
		// The stack's own text can defeat every export just printed: a
		// scaleway_object_* resource reaches the real s3.<region>.scw.cloud
		// no matter what SCW_API_URL says (measured, #262/#280). env is
		// eval'd from the stack directory, which makes this the last moment
		// a warning can land before the apply that escapes. Same contract as
		// above: stderr only, and a directory with no Terraform files stays
		// silent. TestEnvNamesTheEscapeInTheStackNextToIt fails without it.
		if hazards, ok := pack.(packStackHazards); ok {
			if dir, err := os.Getwd(); err == nil {
				if config, files := stackConfig(dir); files > 0 {
					for _, warning := range hazards.StackHazards(config) {
						fmt.Fprintf(stderr, "warning: %s\n", warning)
					}
				}
			}
		}
	}
	return exitOK
}

// packEnv is the slice of a pack this command uses. Declared here rather than
// taking emulator.Pack whole, so it is obvious that env reads one method and
// nothing else.
type packEnv interface {
	Name() string
	Env(endpoint string) emulator.Environment
}

// packEnvClients is the optional half of a pack whose clients disagree about a
// value, so that one printed environment cannot serve them all. Outscale is
// the measured case (#286): OSC_ENDPOINT_API carries the /api/v1 path for the
// Terraform provider >= 1.7 and must not for oapi-cli and the 1.1.x provider.
//
// Optional and declared here rather than on emulator.Pack: the core has no
// business knowing that a provider's clients quarrel, and a pack whose clients
// agree (Scaleway) should not have to implement a method to say so.
type packEnvClients interface {
	// EnvFor answers the environment for one named client family, ok=false
	// when the family is unknown.
	EnvFor(endpoint, client string) (emulator.Environment, bool)
	// EnvClients names the families EnvFor accepts, for the refusal message.
	EnvClients() []string
}

// packEnvHazards is the optional half of a pack that can recognise, in the
// caller's environment, a value that reroutes its clients to the real cloud
// regardless of what env prints. The variable names are provider knowledge and
// live in the pack; this command only carries their warnings to stderr.
type packEnvHazards interface {
	EnvHazards(lookup func(string) (string, bool)) []string
}

// shellRenderer writes an assignment and its removal in one shell's syntax.
//
// Three shells because the three exist and their syntaxes share nothing:
// `export K=v`, `set -gx K v`, `$env:K = "v"`. A user on fish who is handed bash
// syntax gets a parse error rather than a hint.
type shellRenderer struct {
	export func(key, value string) string
	unset  func(key string) string
}

var renderers = map[string]shellRenderer{
	"bash": {
		// Single-quoted, with embedded quotes escaped the only way bash allows:
		// close the quote, insert an escaped one, reopen. None of the values
		// here contain a quote today, and a renderer that breaks the day one
		// does is a renderer that breaks silently.
		export: func(key, value string) string {
			return "export " + key + "='" + strings.ReplaceAll(value, "'", `'\''`) + "'"
		},
		unset: func(key string) string { return "unset " + key },
	},
	"fish": {
		export: func(key, value string) string {
			return "set -gx " + key + " '" + strings.ReplaceAll(value, "'", `\'`) + "'"
		},
		unset: func(key string) string { return "set -e " + key },
	},
	"powershell": {
		export: func(key, value string) string {
			return "$env:" + key + ` = "` + strings.ReplaceAll(value, `"`, "`\"") + `"`
		},
		unset: func(key string) string { return "Remove-Item Env:\\" + key + " -ErrorAction SilentlyContinue" },
	},
}

// defaultEndpoint is the address `serve` listens on by default, rendered as a
// URL a client can use. A bare ":4599" is a valid listen address and not a valid
// endpoint, so the host is filled in.
func defaultEndpoint() string {
	addr := DefaultAddr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr
}

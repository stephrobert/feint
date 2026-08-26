package cli

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/environment"
)

// No Terraform for Exoscale (#525), and the refusal lives on the client side,
// before a process starts.
//
// The incident that decided this never touched the emulator: `feint down` on
// the Exoscale stack resolved the published provider and its refresh sent five
// signed requests to api-ch-*.exoscale.com — the server-side user-agent guard
// cannot see traffic that leaves for the real cloud. So what these tests hold
// is the doorstep: the pack's veto answered in `up`'s preflight and before
// `down`'s destroy, with the same property the runtime refusal has, proved the
// same way — **nothing was started**, evidenced by the log file a spawn
// creates before its child can even fail.

// freeInstance reserves a free port and returns its address and the instance
// directory a spawn on that address would create, cleaned up either way.
func freeInstance(t *testing.T) (addr, dir string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	addr = listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	dir, err = instanceDir(addr)
	if err != nil {
		t.Fatalf("instance directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		// Under the mutation this verb really does start something, and a
		// child left holding a port is how one test starts reporting
		// another's timing.
		var discard strings.Builder
		stop([]string{"--addr", addr}, &discard, &discard)
	})
	return addr, dir
}

// The refusal, on every engine the schema accepts, because OpenTofu resolves
// the same published provider. Host-independent on purpose: the veto is a
// policy question, so it is asked before the host ones, and this test passes
// on a machine with no terraform binary at all.
//
// TestUpRefusesAVetoedEngineBeforeStartingAnything fails when the veto call is
// removed from preflight, and its message assertions fail when the veto stops
// naming the issue or the client that remains.
func TestUpRefusesAVetoedEngineBeforeStartingAnything(t *testing.T) {
	for _, engine := range environment.Engines {
		t.Run(engine, func(t *testing.T) {
			addr, dir := freeInstance(t)

			work := t.TempDir()
			declaration := "version: 1\ncloud:\n  provider: exoscale\nemulator:\n  addr: " + addr +
				"\niac:\n  engine: " + engine + "\n  directory: .\n"
			if err := os.WriteFile(filepath.Join(work, environment.DefaultFile), []byte(declaration), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Chdir(work)

			var out, errOut bytes.Buffer
			if code := Run([]string{"feint", "up"}, &out, &errOut); code != exitError {
				t.Fatalf("exited %d, want %d: an engine the pack vetoes must be refused", code, exitError)
			}
			said := errOut.String()
			// The reason, the upstream issue that unblocks it, and the client
			// that remains. A refusal that states the wall and not the door is
			// one people route around by copying the emulator.
			for _, want := range []string{"#573", "#525", "exo", engine} {
				if !strings.Contains(said, want) {
					t.Errorf("the refusal never says %q: %q", want, said)
				}
			}
			// The doors up itself owns.
			for _, door := range []string{"Nothing was started", "feint up --no-iac", "iac.engine"} {
				if !strings.Contains(said, door) {
					t.Errorf("the refusal offers no way on: %q is missing from %q", door, said)
				}
			}
			// The property that makes it a refusal rather than a message:
			// `spawn` creates the instance directory and its log before the
			// child can fail, so the log's absence is the evidence that
			// nothing was started — neither an emulator nor an engine, which
			// only runs after the start.
			if _, err := os.Stat(filepath.Join(dir, "feint.log")); err == nil {
				t.Errorf("up spawned an emulator on %s before refusing the engine it could see beforehand", addr)
			}
		})
	}
}

// The accepting half: the same declaration without the engine passes the same
// preflight, because `feint up --no-iac` and an engine-less declaration are
// the doors the refusal names, and a doorstep that refused those would serve
// nobody.
func TestPreflightPassesAnExoscaleDeclarationWithoutTheEngine(t *testing.T) {
	decl, err := environment.Parse("version: 1\ncloud:\n  provider: exoscale\nruntime:\n  mode: off\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out bytes.Buffer
	if err := preflight(decl, false, &out); err != nil {
		t.Fatalf("preflight refused a declaration that asks for no engine: %v", err)
	}
	// And --no-iac opens the same door with the engine still declared.
	withEngine, err := environment.Parse("version: 1\ncloud:\n  provider: exoscale\n" +
		"iac:\n  engine: terraform\n  directory: .\nruntime:\n  mode: off\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := preflight(withEngine, true, &out); err != nil {
		t.Fatalf("preflight refused the --no-iac door its own refusal names: %v", err)
	}
}

// `down` was the verb of the incident: #525's five requests left during a
// destroy's refresh. So the veto covers it too — the engine never becomes a
// process — and the skip is said out loud rather than reported as a failure,
// because the teardown this verb owes still happens: the emulator's own state
// dies with the stop, and there is nothing else, since no engine was ever
// allowed to build anything.
//
// TestDownSkipsAVetoedEngineOutLoudAndNeverRunsIt fails when the veto check is
// removed from down.
func TestDownSkipsAVetoedEngineOutLoudAndNeverRunsIt(t *testing.T) {
	work := t.TempDir()
	declaration := "version: 1\ncloud:\n  provider: exoscale\n" +
		"emulator:\n  addr: 127.0.0.1:4614\niac:\n  engine: terraform\n  directory: .\n"
	if err := os.WriteFile(filepath.Join(work, environment.DefaultFile), []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	// --keep-emulator, so the only subject here is the engine decision: there
	// is no emulator on that port, and a stop's own error would be another
	// test's timing wearing this one's name.
	var out, errOut bytes.Buffer
	if code := Run([]string{"feint", "down", "--keep-emulator"}, &out, &errOut); code != exitOK {
		t.Fatalf("exited %d, want %d: a vetoed engine is a loud skip, not a failure — the teardown "+
			"still happened\nstdout: %s\nstderr: %s", code, exitOK, out.String(), errOut.String())
	}
	said := out.String()
	if !strings.Contains(said, "destroy skipped") || !strings.Contains(said, "#573") {
		t.Errorf("the skip is silent or nameless: %q", said)
	}
	// The engine never ran: runEngine announces every step it takes, and a
	// terraform destroy that ran here would have reached the real resolver.
	if strings.Contains(said, "destroy in") || strings.Contains(said, "init in") {
		t.Errorf("down ran the engine it was supposed to refuse: %q", said)
	}
}

// The boundary the veto must not cross: only the Exoscale pack vetoes, and
// only its engines. Scaleway and Outscale stay driven by `feint up` and the
// two engines on every pull request, so a veto leaking onto them would break
// the providers this decision explicitly leaves alone.
func TestOnlyTheExoscalePackVetoesAnEngine(t *testing.T) {
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	vetoed := map[string]bool{}
	for _, p := range srv.Packs() {
		veto, ok := p.(packEngineVeto)
		if !ok {
			continue
		}
		for _, engine := range environment.Engines {
			if veto.VetoEngine(engine) != "" {
				vetoed[p.Name()] = true
			}
		}
	}
	// The witness half: a control that found no veto anywhere would be a
	// control that looked nowhere.
	if !vetoed["exoscale"] {
		t.Error("the exoscale pack vetoes nothing: the doorstep of #525 is disarmed")
	}
	delete(vetoed, "exoscale")
	if len(vetoed) != 0 {
		t.Errorf("packs this decision leaves alone veto an engine: %v", vetoed)
	}
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The station of 2026-11-15 (#974): Pop!_OS 24.04, Incus 7.6 from Zabbly and
// answering, OVN and Open vSwitch absent, Incus never initialised, no image.
func bareStation() station {
	var names []string
	for _, spec := range machine.RequiredImages() {
		names = append(names, spec.Name)
	}
	return station{
		distro:         parseOSRelease("PRETTY_NAME=\"Pop!_OS 24.04 LTS\"\nID=pop\nID_LIKE=\"ubuntu debian\"\n"),
		incusInstalled: true, incusVersion: []int{7, 6, 0}, incusAnswers: true,
		bridges:    map[string]map[string]string{},
		imagesRead: true, missingImages: names,
	}
}

// readyStation is this station once done: every check passes. The Open vSwitch
// settings are unreadable, as they are for a user on Debian, so the wiring is
// judged from br-int.
func readyStation() station {
	s := bareStation()
	s.ovsInstalled, s.ovnInstalled, s.nbSocket, s.brInt = true, true, true, true
	s.pools = []string{"default"}
	s.bridges = map[string]map[string]string{"incusbr0": {}}
	s.missingImages = nil
	return s
}

func byID(checks []check, id string) check {
	for _, c := range checks {
		if c.id == id {
			return c
		}
	}
	return check{}
}

func missingSteps(checks []check) []string {
	var ids []string
	for _, c := range checks {
		if c.step && c.state != verdictOK {
			ids = append(ids, c.id)
		}
	}
	return ids
}

func TestParseOSReleaseNamesTheFamilyFromIDLike(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"Pop!_OS names Ubuntu in ID_LIKE", "ID=pop\nID_LIKE=\"ubuntu debian\"\n", "debian"},
		{"Mint", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n", "debian"},
		{"Debian", "ID=debian\n", "debian"},
		{"Fedora", "ID=fedora\n", "fedora"},
		{"Rocky", "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n", "rocky"},
		// An unrecognised system gets no commands at all, never Debian's.
		{"Arch is not guessed", "ID=arch\n", ""},
	}
	for _, c := range cases {
		if got := parseOSRelease(c.text).family; got != c.want {
			t.Errorf("%s: family %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDoctorListsEveryMissingStepInOnePass is the closing criterion Stéphane
// added on 2026-11-15: one run, every missing step, in the order of the work.
func TestDoctorListsEveryMissingStepInOnePass(t *testing.T) {
	checks := setupChecks(bareStation(), "incus-ovn")
	want := []string{stepOVN, stepWiring, stepInit, stepImages}
	if got := missingSteps(checks); !reflect.DeepEqual(got, want) {
		t.Fatalf("missing steps %v, want %v in this order", got, want)
	}

	var out bytes.Buffer
	renderDoctor(&out, checks, doctorView{mode: "incus-ovn", distro: bareStation().distro})
	text := out.String()
	order := []string{
		"1. OVN and Open vSwitch are not installed.",
		"2. Open vSwitch is not connected to OVN. (do step 1 first)",
		"3. Incus is not initialised: no storage pool, no network bridge.",
		"4. 6 machine image(s) missing",
		"(do step 3 first)",
		"4 steps missing for --vm incus-ovn.",
	}
	at := 0
	for _, piece := range order {
		i := strings.Index(text[at:], piece)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", piece, text)
		}
		at += i + len(piece)
	}

	// The accepting half: the same station, done, has no step left and says so.
	ready := setupChecks(readyStation(), "incus-ovn")
	if got := missingSteps(ready); len(got) != 0 {
		t.Fatalf("a ready station still misses %v", got)
	}
	out.Reset()
	renderDoctor(&out, ready, doctorView{mode: "incus-ovn"})
	if !strings.Contains(out.String(), "Everything --vm incus-ovn needs is present.") {
		t.Fatalf("a ready station is not said to be ready:\n%s", out.String())
	}
}

// One case per remedy, each asserting the exact command printed.
func TestEachRemedyIsTheExactCommand(t *testing.T) {
	debianOVN := []string{"sudo apt-get install -y --no-install-recommends ovn-central ovn-host openvswitch-switch"}
	wiring := []string{
		"sudo systemctl enable --now openvswitch-switch ovn-central ovn-host",
		"while [ ! -S /run/ovn/ovnnb_db.sock ]; do sleep 1; done",
		"sudo ovs-vsctl set open_vswitch . \\",
		"  external_ids:ovn-remote=unix:/run/ovn/ovnsb_db.sock \\",
		"  external_ids:ovn-encap-type=geneve \\",
		"  external_ids:ovn-encap-ip=127.0.0.1",
	}
	bare := setupChecks(bareStation(), "incus-ovn")
	for _, c := range []struct {
		id   string
		want []string
		root bool
	}{
		{stepOVN, debianOVN, true},
		{stepWiring, wiring, true},
		{stepInit, []string{"incus admin init --minimal"}, false},
		{stepImages, []string{"feint images"}, false},
	} {
		got := byID(bare, c.id)
		if !reflect.DeepEqual(got.commands, c.want) || got.root != c.root {
			t.Errorf("%s: commands %q root %v, want %q root %v", c.id, got.commands, got.root, c.want, c.root)
		}
	}

	// Incus absent on Debian: the Zabbly channel, ending on the install.
	noIncus := bareStation()
	noIncus.incusInstalled = false
	incus := byID(setupChecks(noIncus, "incus-ovn"), stepIncus)
	if len(incus.commands) == 0 || incus.commands[0] != "sudo apt-get update" ||
		incus.commands[len(incus.commands)-1] != "sudo apt-get install -y --no-install-recommends incus incus-client" ||
		!strings.Contains(strings.Join(incus.commands, "\n"), "https://pkgs.zabbly.com/incus/stable") {
		t.Errorf("Incus absent: %q", incus.commands)
	}
	// Too old: the same commands, and a title naming both versions.
	old := bareStation()
	old.incusVersion = []int{6, 0, 0}
	tooOld := byID(setupChecks(old, "incus-ovn"), stepIncus)
	if tooOld.title != "Incus 6.0.0 is too old: 6.0.4 or later is needed." || !reflect.DeepEqual(tooOld.commands, incus.commands) {
		t.Errorf("Incus too old: %q %q", tooOld.title, tooOld.commands)
	}
	// Fedora gets dnf, never apt.
	fedora := bareStation()
	fedora.distro = parseOSRelease("ID=fedora\n")
	if got := byID(setupChecks(fedora, "incus-ovn"), stepOVN).commands; !reflect.DeepEqual(got, []string{"sudo dnf install -y ovn ovn-central ovn-host openvswitch"}) {
		t.Errorf("Fedora OVN: %q", got)
	}
	// An unknown system gets no command written for another one.
	arch := bareStation()
	arch.distro = parseOSRelease("ID=arch\nPRETTY_NAME=Arch\n")
	if got := byID(setupChecks(arch, "incus-ovn"), stepOVN); len(got.commands) != 0 || !strings.Contains(got.detail, "No install command is known") {
		t.Errorf("unknown system: %q / %q", got.commands, got.detail)
	}
	// Incus that does not answer its user: the group.
	silent := bareStation()
	silent.incusAnswers, silent.incusErr = false, "permission denied"
	if got := byID(setupChecks(silent, "incus-ovn"), stepGroup); !reflect.DeepEqual(got.commands,
		[]string{`sudo usermod -aG incus-admin "$USER"`, "newgrp incus-admin"}) {
		t.Errorf("incus-admin group: %q", got.commands)
	}
}

// The wiring check has more answers than yes and no, and each case below
// passes every other question and fails only its own.
func TestTheWiringCheckTellsUnknownFromAbsent(t *testing.T) {
	wired := map[string]string{"ovn-remote": "unix:/run/ovn/ovnsb_db.sock", "ovn-encap-type": "geneve", "ovn-encap-ip": "127.0.0.1"}
	cases := []struct {
		name  string
		edit  func(*station)
		state verdict
		title string
	}{
		{"settings read and set", func(s *station) { s.ovsIDs, s.brInt = wired, false }, verdictOK, "Open vSwitch is connected to OVN (ovn-remote unix:/run/ovn/ovnsb_db.sock)."},
		{"settings read, a key missing", func(s *station) { s.ovsIDs = map[string]string{"ovn-remote": "x", "ovn-encap-type": "geneve"} }, verdictWarn, "Open vSwitch is not connected to OVN: ovn-remote, ovn-encap-type or ovn-encap-ip is missing."},
		{"settings unreadable, br-int up", func(*station) {}, verdictOK, "Open vSwitch is connected to OVN (bridge br-int is up)."},
		{"settings unreadable, no br-int", func(s *station) { s.brInt = false }, verdictWarn, "Doctor cannot check the OVN wiring without sudo."},
		{"OVN not running", func(s *station) { s.nbSocket = false }, verdictWarn, "OVN is not running: /run/ovn/ovnnb_db.sock does not exist."},
	}
	for _, c := range cases {
		s := readyStation()
		c.edit(&s)
		got := byID(setupChecks(s, "incus-ovn"), stepWiring)
		if got.state != c.state || got.title != c.title {
			t.Errorf("%s: %v %q, want %v %q", c.name, got.state, got.title, c.state, c.title)
		}
		if got.state != verdictOK && len(got.commands) == 0 {
			t.Errorf("%s: no command", c.name)
		}
	}
}

func fakeTools(present map[string]string) toolProbe {
	return toolProbe{
		goos: "linux", goarch: "amd64",
		lookPath: func(name string) (string, error) {
			if _, ok := present[name]; ok {
				return "/fake/" + name, nil
			}
			return "", errors.New("absent")
		},
		output: func(name string, _ ...string) string { return present[name] },
	}
}

func TestClientsAndToolsNameTheirPinnedVersion(t *testing.T) {
	checks := clientChecks(fakeTools(map[string]string{"scw": "Version 2.56.3", "octl": "octl version v0.0.31", "jq": "jq-1.7"}))
	if c := byID(checks, "client-scw"); c.state != verdictOK || c.title != "scw 2.56.3" {
		t.Errorf("scw at the pin: %+v", c)
	}
	if c := byID(checks, "client-octl"); c.state != verdictWarn || c.title != "octl 0.0.31 is installed; the suites use 0.0.32." {
		t.Errorf("octl off the pin: %+v", c)
	}
	if c := byID(checks, "client-exo"); c.state != verdictWarn || c.title != "exo is missing. The suites use 1.95.6, for the Exoscale CLI suite." || c.fixKey != "clients" {
		t.Errorf("exo missing: %+v", c)
	}

	tools := toolChecks(fakeTools(map[string]string{}))
	// A fresh account has no ~/.local/bin and does not have it on PATH until
	// its next login: the manual install makes the one and exports the other.
	if c := byID(tools, "tool-uv"); !strings.Contains(c.title, "0.13.5") || len(c.commands) != 6 ||
		c.commands[0] != "mkdir -p ~/.local/bin" ||
		c.commands[1] != "curl -fsSLO https://github.com/astral-sh/uv/releases/download/0.13.5/uv-x86_64-unknown-linux-gnu.tar.gz" ||
		!strings.HasPrefix(c.commands[5], `export PATH="$HOME/.local/bin:$PATH"`) {
		t.Errorf("uv missing, no mise: %+v", c)
	}
	pin := []string{"uv tool install --force ansible-core==2.22.0"}
	if c := byID(tools, "tool-ansible"); !reflect.DeepEqual(c.commands, pin) || c.after != "do the uv step first" {
		t.Errorf("Ansible missing: %+v", c)
	}
	// The platform decides the uv build, and an unknown one gets no command.
	arm := fakeTools(map[string]string{})
	arm.goarch = "arm64"
	if c := byID(toolChecks(arm), "tool-uv"); len(c.commands) != 6 || !strings.Contains(c.commands[1], "uv-aarch64-unknown-linux-gnu.tar.gz") {
		t.Errorf("uv on arm64: %+v", c)
	}
	riscv := fakeTools(map[string]string{})
	riscv.goarch = "riscv64"
	if c := byID(toolChecks(riscv), "tool-uv"); len(c.commands) != 0 || !strings.Contains(c.detail, "linux/riscv64") {
		t.Errorf("uv on a platform uv has no build for: %+v", c)
	}
	// Ansible at the pin passes; at another version it is named, with the pin.
	at := toolChecks(fakeTools(map[string]string{"uv": "uv 0.13.5", "mise": "", "ansible-playbook": "ansible-playbook [core 2.22.0]"}))
	if c := byID(at, "tool-ansible"); c.state != verdictOK || c.title != "Ansible (ansible-core 2.22.0)" {
		t.Errorf("Ansible at the pin: %+v", c)
	}
	off := toolChecks(fakeTools(map[string]string{"uv": "uv 0.13.5", "mise": "", "ansible-playbook": "ansible-playbook [core 2.17.1]"}))
	if c := byID(off, "tool-ansible"); c.state != verdictWarn || !strings.Contains(c.title, "2.17.1") || !reflect.DeepEqual(c.commands, pin) || c.after != "" {
		t.Errorf("Ansible off the pin: %+v", c)
	}
	withMise := toolChecks(fakeTools(map[string]string{"mise": "", "uv": "uv 0.13.5 (x)"}))
	if c := byID(withMise, "tool-uv"); c.state != verdictOK || c.title != "uv 0.13.5" {
		t.Errorf("uv at the pin: %+v", c)
	}
}

// The versions doctor names are the ones CI drives and mise pins.
func TestDoctorClientPinsAreTheWorkflows(t *testing.T) {
	pinned, err := pinnedVersions("../../.github/workflows/conformance.yml")
	if err != nil {
		t.Fatalf("read the workflow: %v", err)
	}
	for _, c := range clientPins {
		if got := pinned[c.workflowKey]; got != strings.TrimPrefix(c.version, "v") {
			t.Errorf("%s: doctor says %s, the workflow pins %q", c.binary, c.version, got)
		}
		if !strings.Contains(c.base+"/"+c.asset, strings.TrimPrefix(c.version, "v")) {
			t.Errorf("%s: the download %s/%s does not carry its version", c.binary, c.base, c.asset)
		}
	}
	mise, err := os.ReadFile("../../mise.toml")
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	if !strings.Contains(string(mise), `uv = "`+uvPin+`"`) {
		t.Errorf("mise.toml does not pin uv %s", uvPin)
	}
}

// Stéphane, 2026-11-16: the playbook is named only when it is there.
func TestTheSummaryNamesThePlaybookOnlyWhenItIsThere(t *testing.T) {
	checks := setupChecks(bareStation(), "incus-ovn")
	var with, without bytes.Buffer
	renderDoctor(&with, checks, doctorView{mode: "incus-ovn", playbook: true})
	renderDoctor(&without, checks, doctorView{mode: "incus-ovn", playbook: false})
	if !strings.Contains(with.String(), "    "+playbookCmd+"\n    "+imagesInNewGroup+"\n") {
		t.Errorf("the playbook is not offered where it exists:\n%s", with.String())
	}
	if strings.Contains(without.String(), "local.yml") || strings.Contains(without.String(), "ansible-playbook") {
		t.Errorf("an absent playbook is offered:\n%s", without.String())
	}
	if !strings.Contains(without.String(), "feint doctor --fix --vm incus-ovn") {
		t.Errorf("without the playbook, --fix is not offered:\n%s", without.String())
	}
}

// The install page is generated from the table doctor prints from: every line
// doctor can print is on the page, and the page is what the generator writes.
func TestTheInstallPageIsGeneratedFromDoctorsCommands(t *testing.T) {
	raw, err := os.ReadFile("../../docs/install.md")
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	page := string(raw)
	for name := range setupBlocks {
		for _, line := range commands(name) {
			if !strings.Contains(page, line) {
				t.Errorf("block %s: %q is printed by doctor and absent from docs/install.md", name, line)
			}
		}
	}
	regenerated, err := spliceSetupDoc(page)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if regenerated != page {
		t.Errorf("docs/install.md is not what the generator writes: run mise run docs:coverage")
	}
	for _, section := range setupDocSections {
		if start, _ := setupMarkers(section.key); !strings.Contains(page, start) {
			t.Errorf("the page lost the %s markers, so nothing holds it to doctor", section.key)
		}
	}
}

func TestAnUnknownModeIsRefusedBeforeAnyProbe(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := doctor([]string{"--vm", "incus-ovm"}, &out, &errOut); code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), `--vm "incus-ovm" is not a mode this binary knows`) {
		t.Fatalf("the refusal does not say why: %q", errOut.String())
	}
}

// fixWorld is a station --fix acts on through fakes: the runner changes the
// station the way the real command would, and records every call.
type fixWorld struct {
	s     station
	calls []string
	fail  string // a command that errors
	inert bool   // commands succeed and change nothing
}

func (f *fixWorld) deps() fixDeps {
	return fixDeps{
		run: func(_ context.Context, name string, args ...string) error {
			line := strings.Join(append([]string{name}, args...), " ")
			f.calls = append(f.calls, line)
			if line == f.fail {
				return errors.New("exit status 1")
			}
			if f.inert {
				return nil
			}
			switch line {
			case "incus admin init --minimal":
				f.s.pools, f.s.bridges = []string{"default"}, map[string]map[string]string{"incusbr0": {}}
			case "feint images":
				f.s.missingImages = nil
			}
			return nil
		},
		recheck:    func() []check { return setupChecks(f.s, "incus-ovn") },
		remove:     func(string) error { return nil },
		ownerAlive: func(string) bool { return false },
		install:    func(context.Context, clientPin) error { return nil },
		clientDir:  "/fake/bin",
		uid:        os.Getuid(),
	}
}

func (f *fixWorld) fix(t *testing.T, tty, yes bool, answer string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	deps := f.deps()
	plan := buildFixPlan(setupChecks(f.s, "incus-ovn"), deps, false)
	code := runFix(context.Background(), &out, strings.NewReader(answer), tty, yes, doctorView{mode: "incus-ovn"}, plan, deps)
	return code, out.String()
}

func TestFixPlansTheBareStationInOrder(t *testing.T) {
	w := &fixWorld{s: bareStation()}
	plan := buildFixPlan(setupChecks(w.s, "incus-ovn"), w.deps(), false)
	var shown []string
	for _, s := range plan.steps {
		shown = append(shown, s.shows...)
	}
	if want := []string{"incus admin init --minimal", "feint images"}; !reflect.DeepEqual(shown, want) {
		t.Errorf("user steps %q, want %q", shown, want)
	}
	root := strings.Join(plan.root, "\n")
	for _, want := range []string{"sudo apt-get install -y --no-install-recommends ovn-central ovn-host openvswitch-switch", "sudo ovs-vsctl set open_vswitch . \\"} {
		if !strings.Contains(root, want) {
			t.Errorf("the root block lacks %q:\n%s", want, root)
		}
	}
}

func TestFixNeverRunsARootStep(t *testing.T) {
	w := &fixWorld{s: bareStation()}
	code, out := w.fix(t, false, true, "")
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if want := []string{"incus admin init --minimal", "feint images"}; !reflect.DeepEqual(w.calls, want) {
		t.Fatalf("ran %q, want exactly %q", w.calls, want)
	}
	if !strings.Contains(out, "feint will NOT run these commands, because they need root.") {
		t.Fatalf("the root block is not printed:\n%s", out)
	}
}

func TestFixStopsAtTheFirstFailure(t *testing.T) {
	w := &fixWorld{s: bareStation(), fail: "incus admin init --minimal"}
	code, out := w.fix(t, false, true, "")
	if code != exitError || len(w.calls) != 1 {
		t.Fatalf("exit %d after %q, want 1 after the first call:\n%s", code, w.calls, out)
	}
	if !strings.Contains(out, "Stopped at step 1. Not run: steps 2 to 2.") {
		t.Fatalf("the stop is not explained:\n%s", out)
	}
}

func TestFixDoesNotTrustAStepWithoutItsRecheck(t *testing.T) {
	w := &fixWorld{s: bareStation(), inert: true}
	code, out := w.fix(t, false, true, "")
	if code != exitError || len(w.calls) != 1 || !strings.Contains(out, "it ran, but the check still says") {
		t.Fatalf("a step that changed nothing was accepted (exit %d, %q):\n%s", code, w.calls, out)
	}
}

func TestFixTwiceChangesNothingTheSecondTime(t *testing.T) {
	w := &fixWorld{s: readyStation()}
	w.s.ovsIDs = nil
	code, out := w.fix(t, true, false, "y\n")
	if code != exitOK || len(w.calls) != 0 || !strings.Contains(out, "Nothing to do") || !strings.Contains(out, "Nothing was changed.") {
		t.Fatalf("a done station was changed (exit %d, %q):\n%s", code, w.calls, out)
	}
	// And after a real first run on the bare user steps.
	w = &fixWorld{s: bareStation()}
	w.s.ovsInstalled, w.s.ovnInstalled, w.s.nbSocket, w.s.brInt = true, true, true, true
	if code, out := w.fix(t, false, true, ""); code != exitOK {
		t.Fatalf("first run failed:\n%s", out)
	}
	first := len(w.calls)
	if _, out := w.fix(t, false, true, ""); len(w.calls) != first || !strings.Contains(out, "Nothing to do") {
		t.Fatalf("the second run acted again (%q):\n%s", w.calls[first:], out)
	}
}

func TestFixRunsNothingWithoutATerminal(t *testing.T) {
	w := &fixWorld{s: bareStation()}
	code, out := w.fix(t, false, false, "y\n")
	if code != exitOK || len(w.calls) != 0 || !strings.Contains(out, "Nothing was run: there is no terminal") {
		t.Fatalf("ran without a terminal (exit %d, %q):\n%s", code, w.calls, out)
	}
	// On a terminal, anything but yes runs nothing; yes runs the plan.
	if _, out := w.fix(t, true, false, "\n"); len(w.calls) != 0 || !strings.Contains(out, "Nothing was run.") {
		t.Fatalf("an empty answer ran the plan (%q)", w.calls)
	}
	if code, _ := w.fix(t, true, false, "y\n"); code != exitOK || len(w.calls) != 2 {
		t.Fatalf("yes did not run the plan (exit %d, %q)", code, w.calls)
	}
}

func TestInstallClientRefusesABadChecksum(t *testing.T) {
	body := []byte("#!/bin/sh\necho Version 2.56.3\n")
	right := sha256Hex(body)
	wrong := "0b5c4a6b3bd2c7b2d1d58b0e3a9e5b3e2b6c2a1f0e9d8c7b6a5f4e3d2c1b0a99"
	answers := func(string) string { return "Version 2.56.3" }
	install := func(pinned, published string, versionOf func(string) string) (string, error) {
		pin := clientPin{binary: "scw", version: "2.56.3", base: "https://example.invalid", asset: "scw_bin", sums: "SUMS", sha256: pinned}
		get := func(_ context.Context, url string) ([]byte, error) {
			if strings.HasSuffix(url, "/SUMS") {
				return []byte(published + "  scw_bin\n"), nil
			}
			return body, nil
		}
		dir := t.TempDir()
		return dir, installClient(context.Background(), pin, dir, get, versionOf)
	}
	// One case per question; each passes the other two.
	for _, c := range []struct {
		name              string
		pinned, published string
		versionOf         func(string) string
		reason            string
	}{
		{"the pin disagrees, the sums file agrees", wrong, right, answers, "pinned"},
		{"no pin at all", "", right, answers, "pinned"},
		{"the sums file disagrees, the pin agrees", right, wrong, answers, "published"},
		{"the binary does not answer with its version", right, right, func(string) string { return "Version 2.56.2" }, "does not answer with version 2.56.3"},
	} {
		dir, err := install(c.pinned, c.published, c.versionOf)
		if err == nil || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s: want a refusal naming %q, got %v", c.name, c.reason, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: something was written although it was refused: %v", c.name, entries)
		}
	}
	// The accepting half: both sums right and the version answered.
	dir, err := install(right, right, answers)
	if err != nil {
		t.Fatalf("the right checksum was refused: %v", err)
	}
	if info, err := os.Stat(dir + "/scw"); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("no executable installed: %v %v", info, err)
	}
}

// Every pin carries its digest: an empty one would refuse every install, and
// a malformed one would never match.
func TestEveryClientPinCarriesItsDigest(t *testing.T) {
	for _, c := range clientPins {
		if len(c.sha256) != 64 || strings.Trim(c.sha256, "0123456789abcdef") != "" {
			t.Errorf("%s: pinned digest %q is not 64 lowercase hex characters", c.binary, c.sha256)
		}
	}
}

// Off linux/amd64 the pinned downloads cannot run, so none is offered.
func TestNoClientDownloadIsOfferedOffItsPlatform(t *testing.T) {
	arm := fakeTools(map[string]string{})
	arm.goarch = "arm64"
	c := byID(clientChecks(arm), "client-scw")
	if c.fixKey != "" || !strings.Contains(c.detail, "built for linux/amd64 and this is linux/arm64") {
		t.Fatalf("a linux/amd64 download is offered on arm64: %+v", c)
	}
	if c := byID(clientChecks(fakeTools(map[string]string{})), "client-scw"); c.fixKey != "clients" {
		t.Fatalf("the download is not offered on linux/amd64: %+v", c)
	}
}

// TestFixSaysWhyAnInstalledClientIsNotSeen is cited by clientOnPath.
func TestFixSaysWhyAnInstalledClientIsNotSeen(t *testing.T) {
	deps := fixDeps{recheck: func() []check {
		return []check{{id: "client-scw", state: verdictWarn, title: "scw is missing."}}
	}}
	target := "/home/you/.local/bin/scw"
	for _, c := range []struct {
		name   string
		look   func(string) (string, error)
		reason string
	}{
		{"not on PATH", func(string) (string, error) { return "", errors.New("absent") }, "/home/you/.local/bin is not on your PATH"},
		{"shadowed", func(string) (string, error) { return "/usr/bin/scw", nil }, "/usr/bin/scw comes first on your PATH"},
	} {
		deps.lookPath = c.look
		if ok, why := clientOnPath(deps, "scw", target); ok || !strings.Contains(why, c.reason) {
			t.Errorf("%s: %v %q, want a refusal naming %q", c.name, ok, why, c.reason)
		}
	}
	deps.recheck = func() []check { return []check{{id: "client-scw", state: verdictOK}} }
	if ok, why := clientOnPath(deps, "scw", target); !ok {
		t.Errorf("a client the report sees was refused: %q", why)
	}
}

// jq is installed by the reader's own package manager, and an unknown system
// gets no command written for another one.
func TestTheJqRemedyFollowsTheDistribution(t *testing.T) {
	for family, want := range map[string][]string{
		"debian": {"sudo apt-get install -y jq"},
		"fedora": {"sudo dnf install -y jq"},
		"rocky":  {"sudo dnf install -y jq"},
		"":       nil,
	} {
		p := fakeTools(map[string]string{})
		p.family = family
		c := byID(clientChecks(p), "client-jq")
		if !reflect.DeepEqual(c.commands, want) || c.state != verdictWarn {
			t.Errorf("family %q: %q, want %q", family, c.commands, want)
		}
		if family == "" && !strings.Contains(c.detail, "knows no command") {
			t.Errorf("an unknown system is not told why it gets no command: %q", c.detail)
		}
	}
}

// The Rocky wiring line works on its own: doctor prints it to a reader whose
// OVN is already installed, so it cannot lean on the install block's $ovs.
func TestTheRockyWiringLineStandsAlone(t *testing.T) {
	rocky := readyStation()
	rocky.distro = parseOSRelease("ID=rocky\n")
	rocky.nbSocket = false
	cmds := byID(setupChecks(rocky, "incus-ovn"), stepWiring).commands
	if len(cmds) == 0 || strings.Contains(cmds[0], `"$ovs"`) || !strings.Contains(cmds[0], "systemctl list-unit-files") {
		t.Fatalf("the Rocky start line depends on another block: %q", cmds)
	}
	// And no step anywhere touches incusbr0's address or ranges any more:
	// feint creates its own uplink (#974, Stéphane 2026-11-16).
	for name, block := range setupBlocks {
		for _, line := range block {
			if strings.Contains(line, "incus network set incusbr0") {
				t.Errorf("block %s still prepares incusbr0: %q", name, line)
			}
		}
	}
}

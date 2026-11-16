package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The setup steps of a station, and the commands that make each one (#974).
//
// On 2026-11-15 a station was prepared by hand from docs/install.md because
// doctor did not follow it: OVN absent, Incus never initialised, Open vSwitch not
// wired, and doctor checked none of the three. Its remedies were prose ("install
// Incus") where a reader needed a command. This file is the answer in two parts:
//
//   - setupBlocks is the one copy of every command docs/install.md shows for
//     installing and wiring the runtime. The page is generated from it
//     (renderSetupDoc, spliced by `mise run docs:coverage`, refused by
//     `docs:check` when it drifts), and doctor prints the same lines. The same
//     command written twice is the drift this repository keeps finding.
//   - probeStation reads the host once, read-only, and setupChecks turns what it
//     read into numbered steps in the order a reader must do them. Every step is
//     reported in one pass: a missing step never hides the ones after it.
//
// TestDoctorListsEveryMissingStepInOnePass fails without the second half, and
// TestTheInstallPageIsGeneratedFromDoctorsCommands without the first.

// distro is what /etc/os-release says, reduced to the family that decides the
// commands. Pop!_OS and Mint name Ubuntu in ID_LIKE, which is why ID alone is
// not read.
type distro struct {
	pretty string
	family string // "debian", "fedora", "rocky", or "" when unknown
}

func (d distro) label() string {
	names := map[string]string{"debian": "Debian/Ubuntu family", "fedora": "Fedora", "rocky": "Rocky/RHEL family"}
	name := d.pretty
	if name == "" {
		name = "unknown system"
	}
	if f, ok := names[d.family]; ok {
		return name + " (" + f + ")"
	}
	return name + " (no install commands known for it)"
}

// parseOSRelease reads the family from the text of /etc/os-release. An
// unrecognised system answers family "": the commands are then not printed for
// a distribution they were never written for (an unrecognised shape refuses,
// it does not pass).
func parseOSRelease(text string) distro {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok {
			continue
		}
		values[key] = strings.Trim(value, `"'`)
	}
	d := distro{pretty: values["PRETTY_NAME"]}
	ids := append([]string{values["ID"]}, strings.Fields(values["ID_LIKE"])...)
	for _, id := range ids {
		switch id {
		case "debian", "ubuntu":
			d.family = "debian"
		case "fedora":
			if values["ID"] == "fedora" {
				d.family = "fedora"
			}
		case "rocky", "rhel", "centos", "almalinux":
			d.family = "rocky"
		}
		if d.family != "" {
			break
		}
	}
	return d
}

// setupBlocks holds every command docs/install.md prints for the runtime. A
// line starting with '#' is a comment: the page shows it, doctor does not.
// Commands that need root say sudo; the ones that act as the user (incus, in
// the incus-admin group) do not.
var setupBlocks = map[string][]string{
	"debian-incus": {
		"sudo apt-get update",
		"sudo apt-get install -y --no-install-recommends ca-certificates curl gpg uidmap",
		"",
		"# The key in its own keyring, never the system trust store: a key in",
		"# /etc/apt/keyrings signs the repository that names it and nothing else.",
		"sudo mkdir -p /etc/apt/keyrings",
		"sudo curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc",
		"gpg --show-keys --with-fingerprint /etc/apt/keyrings/zabbly.asc   # check what you just trusted",
		"",
		// No heredoc: doctor indents what it prints, an indented EOF never
		// closes a heredoc, and deb822 reads an indented line as the
		// continuation of the one before. TestNoPrintedCommandDependsOnItsIndentation
		// pastes this block indented and reads the file it writes.
		`printf '%s\n' 'Enabled: yes' 'Types: deb' 'URIs: https://pkgs.zabbly.com/incus/stable' \`,
		`  "Suites: $(. /etc/os-release && echo "$VERSION_CODENAME")" 'Components: main' \`,
		`  "Architectures: $(dpkg --print-architecture)" 'Signed-By: /etc/apt/keyrings/zabbly.asc' \`,
		`  | sudo tee /etc/apt/sources.list.d/zabbly-incus.sources >/dev/null`,
		"",
		"sudo apt-get update",
		"sudo apt-get install -y --no-install-recommends incus incus-client",
	},
	"debian-ovn": {
		"sudo apt-get install -y --no-install-recommends ovn-central ovn-host openvswitch-switch",
	},
	"fedora-incus": {
		"sudo dnf install -y incus incus-agent",
		"sudo systemctl enable --now incus.socket incus.service",
	},
	"fedora-ovn": {
		"sudo dnf install -y ovn ovn-central ovn-host openvswitch",
	},
	"rocky-incus": {
		"# The key before the package it signs. Chicken and egg otherwise: epel-release is",
		"# signed by the EPEL key, and that key arrives *with* epel-release.",
		"sudo rpm --import https://dl.fedoraproject.org/pub/epel/RPM-GPG-KEY-EPEL-9",
		"sudo dnf install -y https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm",
		"sudo dnf config-manager --set-enabled crb",
		"",
		"# Incus is not packaged for Enterprise Linux by EPEL or by Zabbly. The Incus",
		"# documentation names one route, and says what it is: an unofficial COPR, \"not an",
		"# official project of Incus nor Rocky Linux\". You are trusting somebody the Incus",
		"# project does not vouch for; it is worth knowing.",
		"sudo dnf install -y 'dnf-command(copr)'",
		"sudo dnf -y copr enable neelc/incus",
		"sudo dnf install -y incus incus-agent",
		"sudo restorecon -R /var/lib/incus   # SELinux stays enforcing; relabel, never setenforce 0",
		"sudo systemctl enable --now incus.socket incus.service",
	},
	"rocky-ovn": {
		"# OVN lives in the CentOS NFV SIG, which versions every package name it",
		"# publishes: there is no `ovn`, there is `ovn24.09`. Read the series off the",
		"# machine rather than pinning it — it moves.",
		"sudo dnf install -y centos-release-nfv-openvswitch",
		"ovn=$(dnf -q list --available 'ovn*-central' | awk '{print $1}' \\",
		"      | grep -E '^ovn[0-9.]+-central' | sed 's/-central.*//' | sort -V | tail -1)",
		"ovs=$(dnf -q list --available 'openvswitch*' | awk '{print $1}' \\",
		"      | grep -E '^openvswitch[0-9.]+\\.' | sed 's/\\.[a-z0-9_]*$//' | sort -V -u | tail -1)",
		`sudo dnf install -y "$ovn" "${ovn}-central" "${ovn}-host" "$ovs"`,
	},
	// Whoever is not in incus-admin gets "permission denied" from every incus
	// command, which is how a freshly installed Incus answers its own user.
	"incus-group": {
		`sudo usermod -aG incus-admin "$USER"`,
		"newgrp incus-admin   # or log out and back in",
	},
	"debian-ovn-start": {"sudo systemctl enable --now openvswitch-switch ovn-central ovn-host"},
	"fedora-ovn-start": {"sudo systemctl enable --now openvswitch ovn-northd ovn-controller"},
	// The RHEL family versions the Open vSwitch unit (openvswitch3.5), so the
	// name is read off the machine here rather than taken from the install
	// block's $ovs: this line has to work on its own, when doctor prints it to
	// a reader whose OVN is already installed. Never run on Rocky by the
	// author of #974.
	"rocky-ovn-start": {`sudo systemctl enable --now "$(systemctl list-unit-files --no-legend 'openvswitch*.service' | awk 'NR==1{print $1}')" ovn-northd ovn-controller`},
	"ovn-wait":        {"while [ ! -S /run/ovn/ovnnb_db.sock ]; do sleep 1; done"},
	"ovs-wire": {
		"sudo ovs-vsctl set open_vswitch . \\",
		"  external_ids:ovn-remote=unix:/run/ovn/ovnsb_db.sock \\",
		"  external_ids:ovn-encap-type=geneve \\",
		"  external_ids:ovn-encap-ip=127.0.0.1",
	},
	"incus-init": {"incus admin init --minimal"},
	"images":     {"feint images"},
}

// commands returns the lines of the named blocks that doctor prints: comments
// and blank lines dropped, the order kept. A name that has no block panics in
// the test that renders every step, never silently at a user's terminal.
func commands(names ...string) []string {
	var out []string
	for _, name := range names {
		block, ok := setupBlocks[name]
		if !ok {
			panic("doctor: no setup block named " + name)
		}
		for _, line := range block {
			if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			out = append(out, stripComment(line))
		}
	}
	return out
}

// stripComment removes a trailing "   # ..." note a page line carries for its
// reader. Only a comment after three spaces, so a '#' inside a command stays.
func stripComment(line string) string {
	if i := strings.Index(line, "   # "); i >= 0 {
		return strings.TrimRight(line[:i], " ")
	}
	return line
}

// hostProbe is everything probeStation needs from the host, so a test can
// describe a station it does not have. Each field reads and never writes.
type hostProbe struct {
	osRelease func() (string, error)
	lookPath  func(string) (string, error)
	exists    func(string) bool
	// run executes a read-only command and returns its standard output.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// images lists the machine images that are missing. Separate because it
	// goes through the driver, which already knows which images it needs.
	images func(ctx context.Context) (missing []string, err error)
}

func realHostProbe() hostProbe {
	return hostProbe{
		osRelease: func() (string, error) {
			b, err := os.ReadFile("/etc/os-release")
			return string(b), err
		},
		lookPath: exec.LookPath,
		exists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed read-only probes of this file
			out, err := cmd.Output()
			var exit *exec.ExitError
			if errors.As(err, &exit) && len(exit.Stderr) > 0 {
				err = fmt.Errorf("%w: %s", err, firstLine(string(exit.Stderr)))
			}
			return out, err
		},
		images: func(ctx context.Context) ([]string, error) {
			return missingImageNames(ctx, machine.Use(machine.NewIncus()))
		},
	}
}

// station is what the probe read. A zero field is "not known", and the checks
// below say so rather than reading it as absent or present.
type station struct {
	distro distro

	incusInstalled bool
	incusVersion   []int // nil when unreadable
	incusAnswers   bool
	incusErr       string

	ovsInstalled bool
	ovnInstalled bool
	nbSocket     bool
	// ovsIDs is nil when Open vSwitch could not be asked (its socket is
	// root-only on Debian: "database connection failed (Permission denied)").
	ovsIDs map[string]string
	brInt  bool

	pools   []string
	bridges map[string]map[string]string // managed bridges: name -> config

	imagesRead    bool
	missingImages []string
	imagesErr     string
}

var ovsKeyValue = regexp.MustCompile(`([a-z0-9-]+)="?([^",}]*)"?`)

func probeStation(ctx context.Context, p hostProbe) station {
	var s station
	if text, err := p.osRelease(); err == nil {
		s.distro = parseOSRelease(text)
	}

	if _, err := p.lookPath("incus"); err == nil {
		s.incusInstalled = true
		if out, err := p.run(ctx, "incus", "version"); err == nil {
			s.incusVersion = parseIncusVersion(string(out))
		}
		if out, err := p.run(ctx, "incus", "query", "/1.0/storage-pools?recursion=1"); err == nil {
			s.incusAnswers = true
			var pools []struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(out, &pools) == nil {
				for _, pool := range pools {
					s.pools = append(s.pools, pool.Name)
				}
			}
		} else {
			s.incusErr = err.Error()
		}
		if s.incusAnswers {
			s.bridges = map[string]map[string]string{}
			if out, err := p.run(ctx, "incus", "query", "/1.0/networks?recursion=1"); err == nil {
				var networks []struct {
					Name    string            `json:"name"`
					Type    string            `json:"type"`
					Managed bool              `json:"managed"`
					Config  map[string]string `json:"config"`
				}
				if json.Unmarshal(out, &networks) == nil {
					for _, n := range networks {
						// A bridge feint made for itself (feint-uplink carries
						// its label) is not the one `incus admin init` makes:
						// counting it read a station that was never
						// initialised as initialised.
						// TestTheProbeDoesNotTakeFeintsUplinkForInit fails
						// without the label test.
						if n.Managed && n.Type == "bridge" && n.Config["user."+machine.LabelKey] == "" {
							s.bridges[n.Name] = n.Config
						}
					}
				}
			}
			if missing, err := p.images(ctx); err == nil {
				s.imagesRead, s.missingImages = true, missing
			} else {
				s.imagesErr = err.Error()
			}
		}
	}

	_, ovsErr := p.lookPath("ovs-vsctl")
	s.ovsInstalled = ovsErr == nil
	_, ovnErr := p.lookPath("ovn-nbctl")
	s.ovnInstalled = ovnErr == nil
	s.nbSocket = p.exists("/run/ovn/ovnnb_db.sock")
	s.brInt = p.exists("/sys/class/net/br-int")
	if s.ovsInstalled {
		if out, err := p.run(ctx, "ovs-vsctl", "--timeout=2", "get", "open_vswitch", ".", "external_ids"); err == nil {
			s.ovsIDs = map[string]string{}
			for _, m := range ovsKeyValue.FindAllStringSubmatch(string(out), -1) {
				s.ovsIDs[m[1]] = m[2]
			}
		}
	}
	return s
}

func parseIncusVersion(out string) []int {
	// The server line enforces; the client line comes first and is the
	// fallback when the daemon does not answer.
	text := out
	if i := strings.Index(out, "Server version:"); i >= 0 {
		text = out[i:]
	}
	m := regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`).FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	v := make([]int, 3)
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v
}

func versionAtLeast(got []int, floor []int) bool {
	for i := range floor {
		g := 0
		if i < len(got) {
			g = got[i]
		}
		if g != floor[i] {
			return g > floor[i]
		}
	}
	return true
}

// Step identifiers, in the order a reader must do them. setupChecks emits them
// in this order and no other: the numbering a reader sees is the order of the
// work.
const (
	stepIncus  = "incus"
	stepGroup  = "incus-group"
	stepOVN    = "ovn"
	stepWiring = "wiring"
	stepInit   = "incus-init"
	stepImages = "images"
)

// familyBlock names a block of the reader's family, or "" when the family is
// unknown: a command written for another system is not printed as if it fit.
func familyBlock(d distro, suffix string) string {
	if d.family == "" {
		return ""
	}
	return d.family + "-" + suffix
}

// incusSource says where the install commands take Incus from, and why, for
// the reader's family. Each family has its own reason: one sentence for all of
// them told a Fedora reader about a Debian repository.
// TestTheIncusRemedySaysWhyForEachFamily fails when a family gets another's.
func incusSource(d distro) string {
	switch d.family {
	case "debian":
		return "Debian and Ubuntu ship older versions, so this uses the Zabbly packages."
	case "fedora":
		return "Fedora's own packages are recent enough."
	case "rocky":
		return "Incus comes from an unofficial COPR: neither EPEL nor Zabbly packages it for Enterprise Linux."
	}
	return ""
}

func unknownFamily(d distro) string {
	return "No install command is known for " + d.label() + "; docs/install.md lists Debian, Ubuntu, Fedora and Rocky."
}

// setupChecks turns what the probe read into the machine checks of a mode, in
// dependency order, every one of them reported.
func setupChecks(s station, mode string) []check {
	wantOVN := mode == "incus-ovn" || mode == "auto"
	// Under auto the OVN steps are optional, said once in place of why
	// incus-ovn needs them: the two sentences together repeated themselves.
	// TestAutoCountsTheOVNStepsAsOptional fails without either half.
	auto := mode == "auto"
	const optionalWhy = "Optional: without it, auto picks a mode where two VPCs can reach each other."
	var out []check

	// Incus itself.
	floor := versionText(incusMinimum[:])
	switch {
	case !s.incusInstalled:
		c := check{id: stepIncus, group: groupMachines, state: verdictWarn, step: true,
			title:  "Incus is not installed.",
			detail: "Every --vm mode runs machines through Incus. " + floor + " or later is needed. " + incusSource(s.distro)}
		if b := familyBlock(s.distro, "incus"); b != "" {
			c.commands, c.root = commands(b), true
		} else {
			c.detail += " " + unknownFamily(s.distro)
		}
		out = append(out, c)
	case s.incusVersion != nil && !versionAtLeast(s.incusVersion, incusMinimum[:]):
		c := check{id: stepIncus, group: groupMachines, state: verdictWarn, step: true,
			title:  fmt.Sprintf("Incus %s is too old: %s or later is needed.", versionText(s.incusVersion), floor),
			detail: "Below " + floor + " a security group attaches and blocks nothing. " + incusSource(s.distro)}
		if b := familyBlock(s.distro, "incus"); b != "" {
			c.commands, c.root = commands(b), true
		} else {
			c.detail += " " + unknownFamily(s.distro)
		}
		out = append(out, c)
	case s.incusVersion == nil:
		out = append(out, check{id: stepIncus, group: groupMachines, state: verdictWarn,
			title: "Incus is installed, but its version could not be read.", detail: "`incus version` gave nothing doctor could parse."})
	default:
		out = append(out, check{id: stepIncus, group: groupMachines, state: verdictOK,
			title: fmt.Sprintf("Incus %s is installed (%s or later is needed).", versionText(s.incusVersion), floor)})
	}

	// The steps that need Incus are reported even when Incus is absent, each
	// saying which step comes first: a bare station hears the whole journey in
	// one run. TestABareStationHearsEveryStepInOnePass fails without them.
	switch {
	case !s.incusInstalled:
		out = append(out, check{id: stepGroup, group: groupMachines, state: verdictWarn, step: true, needs: []string{stepIncus},
			title:    "You are not in the incus-admin group yet.",
			detail:   "Incus gives full access only to members of incus-admin. The group applies to a new login: log out and back in, or run newgrp in each terminal.",
			commands: commands("incus-group"), root: true})
	case !s.incusAnswers:
		out = append(out, check{id: stepGroup, group: groupMachines, state: verdictWarn, step: true, needs: []string{stepIncus},
			title:    "You cannot use Incus yet.",
			detail:   "You are probably not in the incus-admin group. Incus said: " + s.incusErr,
			commands: commands("incus-group"), root: true})
	}

	if wantOVN {
		switch {
		case !s.ovsInstalled || !s.ovnInstalled:
			c := check{id: stepOVN, group: groupMachines, state: verdictWarn, step: true, optional: auto,
				title:  "OVN and Open vSwitch are not installed.",
				detail: "Without them --vm incus-ovn cannot start: it is the only mode that keeps two VPCs apart."}
			if auto {
				c.detail = optionalWhy
			}
			if b := familyBlock(s.distro, "ovn"); b != "" {
				c.commands, c.root = commands(b), true
			} else {
				c.detail += " " + unknownFamily(s.distro)
			}
			out = append(out, c)
		default:
			out = append(out, check{id: stepOVN, group: groupMachines, state: verdictOK, title: "OVN and Open vSwitch are installed."})
		}
		w := wiringCheck(s)
		if auto && w.state != verdictOK {
			w.optional, w.detail = true, strings.Replace(w.detail, wiringWhy, optionalWhy, 1)
		}
		out = append(out, w)
	}

	switch {
	case !s.incusInstalled || !s.incusAnswers:
		out = append(out, check{id: stepInit, group: groupMachines, state: verdictWarn, step: true, needs: []string{stepGroup},
			title:    "Doctor cannot check the Incus setup yet.",
			detail:   "Machines need a storage pool for their disks, and `feint images` needs a network bridge to download packages.",
			commands: commands("incus-init")})
	case len(s.pools) == 0 || len(s.bridges) == 0:
		var missing []string
		if len(s.pools) == 0 {
			missing = append(missing, "no storage pool")
		}
		if len(s.bridges) == 0 {
			missing = append(missing, "no network bridge")
		}
		out = append(out, check{id: stepInit, group: groupMachines, state: verdictWarn, step: true,
			title:    "Incus is not initialised: " + strings.Join(missing, ", ") + ".",
			detail:   "Machines need a storage pool for their disks, and `feint images` needs the bridge to download packages.",
			commands: commands("incus-init")})
	default:
		names := make([]string, 0, len(s.bridges))
		for name := range s.bridges {
			names = append(names, name)
		}
		sortStrings(names)
		out = append(out, check{id: stepInit, group: groupMachines, state: verdictOK,
			title: fmt.Sprintf("Incus is initialised: storage pool %q, bridge %s.", s.pools[0], strings.Join(names, ", "))})
	}

	out = append(out, imagesCheck(s))
	return out
}

// wiringWhy is why --vm incus-ovn needs the wiring; under auto it gives way
// to the optional sentence.
const wiringWhy = "Without this, creating a subnet fails, and the error does not say why."

func wiringCheck(s station) check {
	c := check{id: stepWiring, group: groupMachines, step: true, needs: []string{stepOVN}, state: verdictWarn,
		detail: wiringWhy}
	start := ""
	if f := familyBlock(s.distro, "ovn-start"); f != "" {
		start = f
	}
	blocks := []string{"ovn-wait", "ovs-wire"}
	if start != "" {
		blocks = append([]string{start}, blocks...)
	}
	c.commands, c.root = commands(blocks...), true

	remote := s.ovsIDs["ovn-remote"]
	switch {
	case !s.ovsInstalled || !s.ovnInstalled:
		c.title = "Open vSwitch is not connected to OVN."
	case s.ovsIDs != nil && remote != "" && s.ovsIDs["ovn-encap-type"] != "" && s.ovsIDs["ovn-encap-ip"] != "" && s.nbSocket:
		return check{id: stepWiring, group: groupMachines, state: verdictOK,
			title: "Open vSwitch is connected to OVN (ovn-remote " + remote + ")."}
	case s.ovsIDs != nil:
		c.title = "Open vSwitch is not connected to OVN: ovn-remote, ovn-encap-type or ovn-encap-ip is missing."
	case !s.nbSocket:
		c.title = "OVN is not running: /run/ovn/ovnnb_db.sock does not exist."
	case s.brInt:
		// Open vSwitch's settings are root-only here, so the answer comes
		// from br-int, which ovn-controller creates only once ovn-remote
		// points at a southbound database it reached.
		return check{id: stepWiring, group: groupMachines, state: verdictOK,
			title:   "Open vSwitch is connected to OVN (bridge br-int is up).",
			verbose: "Its settings need root to read, so this was judged from br-int, which ovn-controller creates only once it reached the southbound database."}
	default:
		c.title = "Doctor cannot check the OVN wiring without sudo."
		c.detail = "The bridge br-int, which appears once Open vSwitch is connected, is absent. Run the commands below. Running them again does no harm. " + wiringWhy
	}
	return c
}

func imagesCheck(s station) check {
	switch {
	case !s.incusInstalled || !s.incusAnswers:
		return check{id: stepImages, group: groupMachines, state: verdictWarn, step: true, needs: []string{stepInit},
			title:    "Doctor cannot check the machine images yet.",
			detail:   "Without them, each machine installs its ssh server at first boot and needs internet access to do it. Building takes a few minutes.",
			commands: commands("images")}
	case !s.imagesRead:
		return check{id: stepImages, group: groupMachines, state: verdictWarn,
			title: "The machine images could not be listed.", detail: s.imagesErr}
	case len(s.missingImages) == 0:
		return check{id: stepImages, group: groupMachines, state: verdictOK, title: "The machine images are present.",
			verbose: "Built by `feint images`, whose recipe installs an ssh daemon: a machine from them answers on port 22 without reaching a package repository."}
	default:
		return check{id: stepImages, group: groupMachines, state: verdictWarn, step: true, needs: []string{stepInit},
			title:    fmt.Sprintf("%d machine image(s) missing: %s.", len(s.missingImages), strings.Join(s.missingImages, ", ")),
			detail:   "Without them, each machine installs its ssh server at first boot and needs internet access to do it. Building takes a few minutes and starts a temporary container.",
			commands: commands("images")}
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// setupDocSections are the code blocks of docs/install.md generated from
// setupBlocks, keyed by their marker. The page shows comments; doctor prints
// the same lines without them. TestTheInstallPageIsGeneratedFromDoctorsCommands
// fails when a block doctor prints is not on the page.
var setupDocSections = []struct {
	key    string
	blocks []string
}{
	{"debian", []string{"debian-incus", "debian-ovn"}},
	{"fedora", []string{"fedora-incus", "fedora-ovn"}},
	{"rocky", []string{"rocky-incus", "rocky-ovn"}},
	{"wiring", []string{"wiring"}},
}

func setupMarkers(key string) (string, string) {
	return "<!-- setup:" + key + ":start -->", "<!-- setup:" + key + ":end -->"
}

// renderSetupDoc writes one generated block of the install page.
func renderSetupDoc(blocks []string) string {
	var b strings.Builder
	b.WriteString(docsGenerated + "\n\n```bash\n")
	for i, name := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		if name == "wiring" {
			b.WriteString(wiringDoc())
			continue
		}
		for _, line := range setupBlocks[name] {
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("```\n")
	return b.String()
}

// wiringDoc is the wiring block: the start line of each family (doctor prints
// the reader's own, uncommented), then the steps that are the same everywhere.
func wiringDoc() string {
	var b strings.Builder
	b.WriteString("# Start them, then wait: the units return before the databases listen.\n")
	b.WriteString(setupBlocks["debian-ovn-start"][0] + "   # Debian/Ubuntu\n")
	b.WriteString("# " + setupBlocks["fedora-ovn-start"][0] + "   # Fedora\n")
	b.WriteString("# " + setupBlocks["rocky-ovn-start"][0] + "   # Rocky 9, never run on Rocky by #974\n")
	for _, name := range []string{"ovn-wait"} {
		b.WriteString(strings.Join(setupBlocks[name], "\n") + "\n")
	}
	b.WriteString("\n# Open vSwitch needs to know where the southbound database is.\n")
	b.WriteString(strings.Join(setupBlocks["ovs-wire"], "\n") + "\n")
	b.WriteString("\n# incus runs as you once you are in incus-admin; the group applies to a new login.\n")
	b.WriteString(strings.Join(setupBlocks["incus-group"], "\n") + "\n")
	b.WriteString(strings.Join(setupBlocks["incus-init"], "\n") + "\n")
	b.WriteString(`
# Incus finds the northbound database on its own: network.ovn.northbound_connection
# defaults to unix:/run/ovn/ovnnb_db.sock, which is where ovn-central puts it.
# Setting it explicitly is a no-op — Incus does not store a key at its default —
# so it is left out here rather than shown as a step that appears to do nothing.
# Only a northbound on another host or another path needs the line.

# No uplink to prepare: feint creates and manages its own, feint-uplink
# (10.209.83.0/24, with its own DHCP and OVN ranges), and its machines sit on
# its own networks. What init gives it is the storage pool for their disks, and
# incusbr0, which only the temporary container of ` + "`feint images`" + ` uses to
# download packages.
`)
	return b.String()
}

// spliceSetupDoc rewrites every generated setup block a document carries.
// A document that carries none is returned unchanged.
func spliceSetupDoc(doc string) (string, error) {
	for _, section := range setupDocSections {
		start, end := setupMarkers(section.key)
		if !strings.Contains(doc, start) {
			continue
		}
		var err error
		doc, err = spliceSection(doc, start, end, renderSetupDoc(section.blocks))
		if err != nil {
			return "", err
		}
	}
	return doc, nil
}

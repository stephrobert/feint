package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// The conformance clients and the repository's own tools (#974).
//
// Before this, a missing client sent the reader to tools/conformance/README.md,
// and uv and Ansible were never named although the playbook needs Ansible and
// the Python tasks need uv. Each now carries its pinned version, what needs it,
// and how to get it.

// clientPin is a conformance client at the version CI drives. The versions are
// the workflow's: TestDoctorClientPinsAreTheWorkflows fails when the two drift,
// the way clientPinMismatches already holds the Ansible role to it.
type clientPin struct {
	binary, version, why string
	workflowKey          string // the *_VERSION key in conformance.yml
	base, asset, sums    string // download base, asset name, checksum file name
	member               string // the binary inside an archive; "" for a bare binary
	// sha256 is the asset's digest, pinned here beside its version and read
	// from the publisher's sums file on 2026-11-16. The download must match
	// it AND the sums file fetched beside it: a sums file served by the same
	// host as the asset proves only that the two were swapped together.
	// TestInstallClientRefusesABadChecksum fails without either comparison.
	sha256 string
}

// clientPlatform is the one platform clientPins' assets are built for. On any
// other, doctor offers no download rather than a binary that cannot run.
const clientPlatform = "linux/amd64"

var clientPins = []clientPin{
	{binary: "scw", version: "2.56.3", why: "the Scaleway CLI suite", workflowKey: "SCW_VERSION",
		base:  "https://github.com/scaleway/scaleway-cli/releases/download/v2.56.3",
		asset: "scaleway-cli_2.56.3_linux_amd64", sums: "SHA256SUMS",
		sha256: "2d5f116e2374692371281372ade47390ac2981e8dcdf9b8c75a1a51d58b75e47"},
	{binary: "octl", version: "v0.0.32", why: "the Outscale CLI suite", workflowKey: "OCTL_VERSION",
		base:  "https://github.com/outscale/octl/releases/download/v0.0.32",
		asset: "octl_Linux_x86_64", sums: "octl_0.0.32_checksums.txt",
		sha256: "0d14612b14cd0425dee1484297a8d374deabe75696bd7a075300a19b5056506c"},
	{binary: "exo", version: "v1.95.6", why: "the Exoscale CLI suite", workflowKey: "EXO_VERSION",
		base:  "https://github.com/exoscale/cli/releases/download/v1.95.6",
		asset: "exoscale-cli_1.95.6_linux_amd64.tar.gz", sums: "exoscale-cli_1.95.6_checksums.txt", member: "exo",
		sha256: "5808b1e034244e5458081c58d013e85255df74e6119dca21c4404275f87213a6"},
	{binary: "terraform", version: "1.13.3", why: "the Terraform suite", workflowKey: "TERRAFORM_VERSION",
		base:  "https://releases.hashicorp.com/terraform/1.13.3",
		asset: "terraform_1.13.3_linux_amd64.zip", sums: "terraform_1.13.3_SHA256SUMS", member: "terraform",
		sha256: "71fc43d92ea09907be5d416d2405a6a9c2d1ceaed633f5e175c0af26e8c4b365"},
	{binary: "tofu", version: "1.12.5", why: "the OpenTofu suite", workflowKey: "TOFU_VERSION",
		base:  "https://github.com/opentofu/opentofu/releases/download/v1.12.5",
		asset: "tofu_1.12.5_linux_amd64.zip", sums: "tofu_1.12.5_SHA256SUMS", member: "tofu",
		sha256: "dade9650e6b74fc7a8b986bd8717497d32f9e09cf82e479afef4977fa3085536"},
}

// uvPin is mise.toml's. TestDoctorClientPinsAreTheWorkflows fails when the two drift.
const uvPin = "0.13.5"

// ansiblePin is the ansible-core the local playbook was written and checked
// with (2.22.0, measured on the station on 2026-11-16 by `ansible-playbook
// --version`). Pinned like every other tool, so "the version doctor installs"
// means one thing. TestClientsAndToolsNameTheirPinnedVersion holds the
// command and the check to it.
const ansiblePin = "2.22.0"

// uvTriple is uv's name for this machine's platform, or "" when uv publishes
// no build doctor knows for it. Read from the binary's own GOARCH, since doctor
// runs on the station it describes.
func uvTriple(goos, goarch string) string {
	if goos != "linux" {
		return ""
	}
	switch goarch {
	case "amd64":
		return "x86_64-unknown-linux-gnu"
	case "arm64":
		return "aarch64-unknown-linux-gnu"
	}
	return ""
}

// toolProbe is what the client and tool checks read: a test describes the
// binaries it wants present.
type toolProbe struct {
	family   string // the distribution family, for the commands that differ
	goos     string // the platform the downloads are for; runtime's in real use
	goarch   string
	lookPath func(string) (string, error)
	output   func(name string, args ...string) string
	// inCheckout is true when doctor runs at the root of a feint clone, the
	// only place the repository's uv pin means anything.
	inCheckout bool
}

func realToolProbe() toolProbe {
	return toolProbe{family: probeDistro(realHostProbe()).family, goos: runtime.GOOS, goarch: runtime.GOARCH,
		lookPath: exec.LookPath, output: runQuiet, inCheckout: inFeintCheckout(".")}
}

// clientsDownloadable answers whether clientPins' assets run here.
func (p toolProbe) clientsDownloadable() bool { return p.goos+"/"+p.goarch == clientPlatform }

// clientFixKey ties a client check to --fix --clients only where the pinned
// download runs.
func (p toolProbe) clientFixKey() string {
	if p.clientsDownloadable() {
		return "clients"
	}
	return ""
}

var toolVersion = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

func clientVersionArgs(binary string) []string {
	if binary == "octl" {
		return []string{"--version"}
	}
	return []string{"version"}
}

// clientChecks reports every client: present at the pin, present at another
// version, or missing. A version doctor cannot read is said, never assumed.
func clientChecks(p toolProbe) []check {
	out := make([]check, 0, len(clientPins)+1)
	for _, c := range clientPins {
		want := strings.TrimPrefix(c.version, "v")
		path, err := p.lookPath(c.binary)
		if err != nil {
			mc := check{id: "client-" + c.binary, group: groupClients, state: verdictWarn,
				title:  fmt.Sprintf("%s is missing. The suites use %s, for %s.", c.binary, want, c.why),
				fixKey: "clients"}
			if !p.clientsDownloadable() {
				mc.fixKey = ""
				mc.detail = "The pinned download is built for " + clientPlatform + " and this is " + p.goos + "/" + p.goarch + ": install it from its publisher."
			}
			out = append(out, mc)
			continue
		}
		raw := firstLine(p.output(c.binary, clientVersionArgs(c.binary)...))
		got := ""
		if m := toolVersion.FindStringSubmatch(raw); m != nil {
			got = m[1]
		}
		switch got {
		case want:
			out = append(out, check{id: "client-" + c.binary, group: groupClients, state: verdictOK,
				title: c.binary + " " + got, verbose: path})
		case "":
			out = append(out, check{id: "client-" + c.binary, group: groupClients, state: verdictWarn,
				title:  fmt.Sprintf("%s is installed, but its version could not be read. The suites use %s.", c.binary, want),
				detail: path + " answered: " + raw})
		default:
			out = append(out, check{id: "client-" + c.binary, group: groupClients, state: verdictWarn,
				title:  fmt.Sprintf("%s %s is installed; the suites use %s.", c.binary, got, want),
				detail: "With another version, a test can fail for a reason that is not in feint. " + path,
				fixKey: p.clientFixKey(), foundAt: path, answered: raw})
		}
	}
	if path, err := p.lookPath("jq"); err != nil {
		c := check{id: "client-jq", group: groupClients, state: verdictWarn, title: "jq is missing. Every suite uses it."}
		switch p.family {
		case "debian":
			c.commands, c.root = []string{"sudo apt-get install -y jq"}, true
		case "fedora", "rocky":
			c.commands, c.root = []string{"sudo dnf install -y jq"}, true
		default:
			c.detail = "Install it with your system's package manager; doctor knows no command for this system."
		}
		out = append(out, c)
	} else {
		out = append(out, check{id: "client-jq", group: groupClients, state: verdictOK,
			title: strings.Replace(firstLine(p.output("jq", "--version")), "jq-", "jq ", 1), verbose: path})
	}
	return out
}

// toolChecks reports uv and Ansible. Neither is needed to serve; the summary
// and these lines say which path needs which.
func toolChecks(p toolProbe) []check {
	var out []check
	uvCmd, uvNote := []string{"mise install uv"}, ""
	if _, err := p.lookPath("mise"); err != nil {
		uvCmd = manualUV(p.goos, p.goarch)
		if uvCmd == nil {
			uvNote = "uv publishes no build doctor knows for " + p.goos + "/" + p.goarch + "; see https://docs.astral.sh/uv/getting-started/installation/."
		}
	}
	if _, err := p.lookPath("uv"); err != nil {
		out = append(out, check{id: "tool-uv", group: groupTools, state: verdictWarn,
			title:    "uv is missing. You need it to install Ansible, and for the repository's Python scripts (version " + uvPin + ").",
			detail:   uvNote,
			commands: uvCmd})
	} else {
		// The pin is the repository's, so it is judged only from a clone:
		// elsewhere `mise install uv` cannot meet it, and uv serves to install
		// Ansible at any version. TestTheUVPinIsJudgedOnlyInAClone fails
		// when it is judged outside one.
		raw := firstLine(p.output("uv", "--version"))
		got := ""
		if m := toolVersion.FindStringSubmatch(raw); m != nil {
			got = m[1]
		}
		state, title, verbose := verdictOK, "uv "+orUnread(got), ""
		switch {
		case got == uvPin:
		case !p.inCheckout:
			verbose = "The repository pins uv " + uvPin + "; that is judged only from the root of a clone."
		case got == "":
			state, title = verdictWarn, "uv is installed, but its version could not be read; the repository pins "+uvPin+"."
		default:
			state, title = verdictWarn, "uv "+got+" is installed; the repository pins "+uvPin+"."
		}
		c := check{id: "tool-uv", group: groupTools, state: state, title: title, verbose: verbose}
		if state != verdictOK {
			c.commands, c.detail = uvCmd, uvNote
		}
		out = append(out, c)
	}
	ansibleCmd := []string{"uv tool install --force ansible-core==" + ansiblePin}
	_, uvErr := p.lookPath("uv")
	if _, err := p.lookPath("ansible-playbook"); err != nil {
		c := check{id: "tool-ansible", group: groupTools, state: verdictWarn,
			title:    "Ansible is missing. Only the one-command setup (the playbook) needs it, at ansible-core " + ansiblePin + ".",
			commands: ansibleCmd}
		if uvErr != nil {
			c.after = "do the uv step first"
		}
		out = append(out, c)
	} else {
		got := ""
		if m := toolVersion.FindStringSubmatch(firstLine(p.output("ansible-playbook", "--version"))); m != nil {
			got = m[1]
		}
		switch got {
		case ansiblePin:
			out = append(out, check{id: "tool-ansible", group: groupTools, state: verdictOK, title: "Ansible (ansible-core " + got + ")"})
		default:
			c := check{id: "tool-ansible", group: groupTools, state: verdictWarn,
				title:    "Ansible is installed (ansible-core " + orUnread(got) + "); the playbook was checked with " + ansiblePin + ".",
				commands: ansibleCmd}
			if uvErr != nil {
				c.after = "do the uv step first"
			}
			out = append(out, c)
		}
	}
	return out
}

func orUnread(version string) string {
	if version == "" {
		return "version unread"
	}
	return version
}

// manualUV is the uv install for a machine without mise: the pinned release,
// its published checksum checked, into ~/.local/bin, which a fresh account
// does not have yet and does not have on PATH until its next login. nil when
// uv publishes no build doctor knows for the platform.
func manualUV(goos, goarch string) []string {
	triple := uvTriple(goos, goarch)
	if triple == "" {
		return nil
	}
	archive := "uv-" + triple + ".tar.gz"
	base := "https://github.com/astral-sh/uv/releases/download/" + uvPin + "/"
	return []string{
		"mkdir -p ~/.local/bin",
		"curl -fsSLO " + base + archive,
		"curl -fsSLO " + base + archive + ".sha256",
		"sha256sum -c " + archive + ".sha256",
		"tar -xzf " + archive + " --strip-components=1 -C ~/.local/bin uv-" + triple + "/uv",
		`export PATH="$HOME/.local/bin:$PATH"   # until your next login`,
	}
}

// clientDownload fetches one URL with a deadline. Injected in tests.
type clientDownload func(ctx context.Context, url string) ([]byte, error)

func httpDownload(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("%s is not https, so nothing was fetched", url)
	}
	client := &http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" {
			return fmt.Errorf("a redirect to %s is not https", next.URL.Redacted())
		}
		if len(via) >= 10 {
			return errors.New("more than 10 redirects")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}

// installClient downloads a pinned client, checks it against the checksum file
// its publisher ships beside it, and writes the binary into dir. Nothing is
// written when the sum does not match or is absent:
// TestInstallClientRefusesABadChecksum fails without that refusal.
func installClient(ctx context.Context, c clientPin, dir string, get clientDownload, versionOf func(path string) string) error {
	sums, err := get(ctx, c.base+"/"+c.sums)
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == c.asset {
			want = strings.ToLower(fields[0])
		}
	}
	if want == "" {
		return fmt.Errorf("%s does not list %s, so nothing was installed", c.sums, c.asset)
	}
	body, err := get(ctx, c.base+"/"+c.asset)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if c.sha256 == "" || got != c.sha256 {
		return fmt.Errorf("sha256 mismatch for %s (got %s, pinned %q): nothing was installed", c.asset, got, c.sha256)
	}
	if got != want {
		return fmt.Errorf("sha256 mismatch for %s (got %s, published %s): nothing was installed", c.asset, got, want)
	}
	binary := body
	switch {
	case strings.HasSuffix(c.asset, ".zip"):
		binary, err = fromZip(body, c.member)
	case strings.HasSuffix(c.asset, ".tar.gz"):
		binary, err = fromTarGz(body, c.member)
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // ~/.local/bin is meant to be on PATH, readable
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+c.binary+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(binary); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // an executable meant to be run by its owner
		return err
	}
	// Asked of the new file before it takes the name: a binary that does not
	// answer with its version never replaces one that worked.
	if out := versionOf(tmp.Name()); !strings.Contains(out, strings.TrimPrefix(c.version, "v")) {
		return fmt.Errorf("the downloaded %s does not answer with version %s, so nothing was installed", c.binary, strings.TrimPrefix(c.version, "v"))
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, c.binary))
}

func fromZip(body []byte, member string) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, err
	}
	for _, f := range r.File {
		if f.Name != member {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(io.LimitReader(rc, 1<<30))
	}
	return nil, fmt.Errorf("the archive holds no %s", member)
}

func fromTarGz(body []byte, member string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive holds no %s", member)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Clean(h.Name) == member {
			return io.ReadAll(io.LimitReader(tr, 1<<30))
		}
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

package environment

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The declaration this repository's own example stacks carry, used wherever a
// test needs a valid file. It is the shape a reader meets, not one invented for
// the test: the same fields examples/stacks/scaleway/feint.yaml declares.
const validDeclaration = `version: 1

cloud:
  provider: scaleway
  projects:
    - default
    - platform-prod

emulator:
  addr: 127.0.0.1:4599
  log_level: info

runtime:
  mode: off

iac:
  engine: terraform
  directory: .
  vars:
    endpoint: ${feint.endpoint}

ready:
  - http:/instance/v1/zones/fr-par-1/servers
  - resource:instance:6
`

func TestAValidDeclarationLoads(t *testing.T) {
	file, err := Parse(validDeclaration)
	if err != nil {
		t.Fatalf("a valid declaration was refused: %v", err)
	}
	if file.Cloud.Provider != "scaleway" {
		t.Errorf("cloud.provider = %q, want scaleway", file.Cloud.Provider)
	}
	if file.Runtime.Mode != "off" {
		t.Errorf("runtime.mode = %q, want off", file.Runtime.Mode)
	}
	if got := file.EngineVars()["endpoint"]; got != "http://127.0.0.1:4599" {
		t.Errorf("the endpoint substitution produced %q, want the emulator's endpoint", got)
	}
	if len(file.Conditions()) != 2 {
		t.Errorf("%d ready conditions parsed, want 2", len(file.Conditions()))
	}
}

// The round trip #189 asks for, and the reason Render is not a courtesy: a
// field added to File and forgotten in Render makes this go red, because the
// rendered copy no longer carries it.
func TestAValidDeclarationSurvivesARoundTrip(t *testing.T) {
	first, err := Parse(validDeclaration)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rendered := Render(first)
	second, err := Parse(rendered)
	if err != nil {
		t.Fatalf("the rendered copy of a valid declaration was refused: %v\n%s", err, rendered)
	}
	first.Path, second.Path = "", ""
	if !reflect.DeepEqual(first, second) {
		t.Errorf("the round trip changed the declaration:\n  first:  %+v\n  second: %+v\n  rendered:\n%s",
			first, second, rendered)
	}
	// And the render is stable, so a diff of two renders means something moved.
	if again := Render(second); again != rendered {
		t.Errorf("two renders of one declaration differ:\n%s\n---\n%s", rendered, again)
	}
}

// The refusal #189 names first: a file somebody mistyped must fail at load, not
// at the first surprising behaviour. Every case below names the field.
func TestAMistypedFieldIsRefusedByName(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "a flag name where a field name belongs",
			src:  "version: 1\nemulator:\n  log-level: debug\n",
			want: "emulator.log-level",
		},
		{
			name: "a misspelled block",
			src:  "version: 1\nruntimes:\n  mode: off\n",
			want: "runtimes",
		},
		{
			name: "a block borrowed from Terraform",
			src:  "version: 1\nnetwork:\n  subnet: 10.0.0.0/24\n",
			want: "network",
		},
		{
			name: "a package list borrowed from Devbox",
			src:  "version: 1\npackages:\n  - terraform\n",
			want: "packages",
		},
		{
			name: "a value where a block belongs",
			src:  "version: 1\nemulator: 127.0.0.1:4599\n",
			want: "emulator",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.src)
			if err == nil {
				t.Fatalf("accepted, and the field would then be discovered later")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal never names %q: %v", tc.want, err)
			}
		})
	}
}

// The other half of every refusal test: the case that must still work. A schema
// that refuses everything passes every test above and serves nobody.
func TestEveryFieldOfTheSchemaIsAccepted(t *testing.T) {
	src := `version: 1
cloud:
  provider: scaleway
  projects:
    - default
    - platform-prod
emulator:
  addr: 127.0.0.1:4699
  state: state.json
  contracts: contracts
  log_level: debug
  cleanup: true
  env:
    FEINT_BOOT_IMAGES: "0"
runtime:
  mode: incus
  images:
    - ubuntu/24.04
snapshot:
  load: fixture
iac:
  engine: opentofu
  directory: terraform
  vars:
    endpoint: ${feint.endpoint}
ready:
  - http:/_feint/health
  - tcp:127.0.0.1:4699
  - resource:instance
`
	file, err := Parse(src)
	if err != nil {
		t.Fatalf("a declaration using every field was refused: %v", err)
	}
	for _, fd := range Fields() {
		if !file.Declared(fd.Path) {
			t.Errorf("`%s` is in the schema and this declaration writes it, and the loader did not record it", fd.Path)
		}
	}
	if len(file.Unread()) != 0 {
		t.Errorf("fields declared and read by nothing: %v — either a verb reads them or the reference says so",
			file.Unread())
	}
}

// What this file deliberately does not carry is refused with the reason, never
// as a plain unknown key: a reader who writes it has a model of what the file
// is for, and "unknown field" leaves that model intact.
func TestWhatIsNotCarriedIsRefusedWithItsReason(t *testing.T) {
	for _, fd := range NotCarried() {
		t.Run(fd.Path, func(t *testing.T) {
			parent, leaf := split(fd.Path)
			src := "version: 1\n"
			if parent == "" {
				src += leaf + ":\n  upstream: https://api.example\n"
			} else {
				src += parent + ":\n  " + leaf + ": true\n"
			}
			_, err := Parse(src)
			if err == nil {
				t.Fatalf("accepted, so the file carries a field nothing acts on")
			}
			if !strings.Contains(err.Error(), fd.Path) {
				t.Errorf("the refusal never names the field: %v", err)
			}
			// The reason, not only the name: this is what tells a reader where
			// the thing they wanted actually lives.
			if !strings.Contains(err.Error(), "is not carried by this file") {
				t.Errorf("the refusal gives no reason: %v", err)
			}
		})
	}
}

// runtime.mode is the --vm values and nothing else, and a name that is not one
// of them is refused **with the list** rather than accepted and discovered when
// the emulator starts.
func TestAnUnknownRuntimeModeIsRefusedWithTheList(t *testing.T) {
	_, err := Parse("version: 1\nruntime:\n  mode: docker\n")
	if err == nil {
		t.Fatal("`mode: docker` was accepted; the Docker driver was removed and this would surface at startup")
	}
	for _, mode := range RuntimeModes {
		if !strings.Contains(err.Error(), mode) {
			t.Errorf("the refusal does not name the mode %q, so a reader cannot fix it: %v", mode, err)
		}
	}
}

// emulator.env sets the emulator's own knobs and nothing else. A declaration
// able to set any variable of a process it spawns is a different and much
// larger thing, and the refusal says both halves.
func TestAnEnvironmentVariableOutsideTheProjectsOwnIsRefused(t *testing.T) {
	for _, name := range []string{"LD_PRELOAD", "PATH", "AWS_SECRET_ACCESS_KEY", "SCW_SECRET_KEY"} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("version: 1\nemulator:\n  env:\n    " + name + ": x\n")
			if err == nil {
				t.Fatalf("a declaration set %s on the process it spawns", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the refusal never names the variable: %v", err)
			}
		})
	}
	// The accepting half, without which a guard that refuses everything would
	// pass every case above.
	file, err := Parse("version: 1\nemulator:\n  env:\n    FEINT_BOOT_IMAGES: \"0\"\n")
	if err != nil {
		t.Fatalf("the project's own knob was refused: %v", err)
	}
	if file.Emulator.Env["FEINT_BOOT_IMAGES"] != "0" {
		t.Errorf("emulator.env = %v, want the declared knob", file.Emulator.Env)
	}
}

// A declaration cannot arm the Exoscale Terraform escape. #525 measured what
// carrying it in a stack's feint.yaml did: the emulator's one refusal was
// lifted for whatever provider the engine resolved — the published one, that
// day — and five signed requests left for api-ch-*.exoscale.com. The variable
// is a hand-export to the shell that runs `feint serve`, or nothing; the
// refusal has to say so, and name the doorstep that refuses the engine anyway.
func TestTheDeclarationCannotArmTheExoscaleTerraformEscape(t *testing.T) {
	_, err := Parse("version: 1\nemulator:\n  env:\n    FEINT_EXOSCALE_ALLOW_TERRAFORM: \"1\"\n")
	if err == nil {
		t.Fatal("a declaration armed FEINT_EXOSCALE_ALLOW_TERRAFORM, which is what let #525 happen")
	}
	for _, want := range []string{"FEINT_EXOSCALE_ALLOW_TERRAFORM", "#525", "by hand", "#573"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never says %q: %v", want, err)
		}
	}
}

func TestAReadyConditionNobodyServesIsRefusedWithTheForms(t *testing.T) {
	for _, raw := range []string{"ssh:web-01", "ping:10.0.0.1", "http:no-leading-slash", "tcp:host", "resource:instance:0"} {
		t.Run(raw, func(t *testing.T) {
			_, err := Parse("version: 1\nready:\n  - " + raw + "\n")
			if err == nil {
				t.Fatalf("accepted, so `up` would wait for a condition nothing evaluates")
			}
		})
	}
	// And the forms that must still work.
	file, err := Parse("version: 1\nready:\n  - http:/_feint/health\n  - tcp:127.0.0.1:4599\n  - resource:instance:3\n")
	if err != nil {
		t.Fatalf("the three forms were refused: %v", err)
	}
	got := file.Conditions()
	if len(got) != 3 {
		t.Fatalf("%d conditions, want 3", len(got))
	}
	if got[2].Count != 3 || got[2].Resource != "instance" {
		t.Errorf("resource:instance:3 parsed as %+v", got[2])
	}
}

func TestAVersionThisBinaryDoesNotReadIsRefusedNamingBoth(t *testing.T) {
	_, err := Parse("version: 2\ncloud:\n  provider: scaleway\n")
	if err == nil {
		t.Fatal("a declaration from the future was accepted")
	}
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "1") {
		t.Errorf("the refusal names only one of the two versions: %v", err)
	}
	if _, err := Parse("cloud:\n  provider: scaleway\n"); err == nil {
		t.Error("a declaration with no version was accepted")
	}
}

// A key written twice would otherwise have one of the two win in silence, which
// is the class of defect this whole file exists to make impossible.
func TestADuplicateKeyIsRefused(t *testing.T) {
	_, err := Parse("version: 1\nruntime:\n  mode: off\n  mode: incus\n")
	if err == nil {
		t.Fatal("a duplicated key was accepted, and one of the two values won in silence")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("the refusal does not say what happened: %v", err)
	}
}

// The YAML this reader does not implement is refused by name rather than
// mis-parsed. A subset that fails on a construct without saying which one
// teaches a reader that the file is broken, not that the reader is.
func TestTheYAMLThisReaderDoesNotImplementIsRefusedByName(t *testing.T) {
	cases := map[string]string{
		"a tab":          "version: 1\nruntime:\n\tmode: off\n",
		"flow style":     "version: 1\nruntime: {mode: off}\n",
		"an anchor":      "version: 1\ncloud:\n  provider: &p scaleway\n",
		"a block scalar": "version: 1\ncloud:\n  provider: |\n    scaleway\n",
		"two documents":  "version: 1\n---\nversion: 1\n",
		"an open quote":  "version: 1\ncloud:\n  provider: \"scaleway\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(src); err == nil {
				t.Fatalf("accepted; the reader would then act on something nobody wrote")
			}
		})
	}
}

// Comments and quoted hashes, because a reader annotates their declaration and
// a reader who quotes a value containing a hash must get the hash.
func TestCommentsAreStrippedAndQuotedHashesAreNot(t *testing.T) {
	file, err := Parse("# what this environment is\nversion: 1 # the schema\ncloud:\n  provider: \"scale#way\"\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if file.Cloud.Provider != "scale#way" {
		t.Errorf("provider = %q; a quoted hash is data, not a comment", file.Cloud.Provider)
	}
}

// Every relative path is relative to the declaration, never to the working
// directory: the same file has to do the same thing wherever it is run from,
// which is the property #192 exists to protect.
func TestPathsResolveAgainstTheDeclarationNotTheCaller(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFile)
	if err := os.WriteFile(path, []byte("version: 1\nemulator:\n  state: state.json\niac:\n  engine: terraform\n  directory: tf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := file.Resolve(file.Emulator.State), filepath.Join(dir, "state.json"); got != want {
		t.Errorf("state resolved to %q, want %q", got, want)
	}
	if got, want := file.Resolve(file.IaC.Directory), filepath.Join(dir, "tf"); got != want {
		t.Errorf("directory resolved to %q, want %q", got, want)
	}
}

// Every field of the schema carries the sentence the reference renders. A field
// added without one would show on the page as a blank cell, which is worse than
// visible: it reads as "nothing to say about this".
func TestEveryFieldOfTheSchemaIsDocumented(t *testing.T) {
	if len(Fields()) < 10 {
		t.Fatalf("only %d fields found: the schema table is not being read", len(Fields()))
	}
	for _, fd := range Fields() {
		if fd.Doc == "" {
			t.Errorf("`%s` has no sentence, so the generated reference shows it blank", fd.Path)
		}
		if fd.Takes == "" {
			t.Errorf("`%s` does not say what it takes", fd.Path)
		}
		if fd.assign == nil {
			t.Errorf("`%s` is documented and nothing reads it", fd.Path)
		}
	}
	for _, fd := range NotCarried() {
		if fd.Doc == "" {
			t.Errorf("`%s` is refused with no reason", fd.Path)
		}
	}
}

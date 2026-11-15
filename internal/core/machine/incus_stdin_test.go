package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// #910: the instance configuration reaches `incus init` on stdin, never on
// argv. /proc/<pid>/cmdline is world-readable for as long as the create runs,
// and the user data is either the rendered cloud-config, authorized keys
// included, or the client's own document — passwords, tokens, write_files.
//
// Two questions, each with its own test, because a test asking both at once
// passes for the wrong reason the day one of them is neutralised:
//
//   - is the payload absent from every argv? TestNoInstanceConfigReachesArgv
//   - does it still arrive, intact? TestTheInstanceConfigArrivesOnStdin
//
// A driver that dropped the user data altogether passes the first and boots a
// machine nobody can log into; one that kept it on argv as well passes the
// second. Proven through the runner, so it says what the driver sends and
// nothing about what Incus accepts: that half is the runtime conformance leg.

// initConfig decodes what the one `init` call was fed on stdin, as the CLI
// would: the config map of api.InstancePut.
func (f *fakeRuntime) initConfig(t *testing.T) map[string]string {
	t.Helper()
	var found []int
	for i, call := range f.calls {
		if len(call) > 0 && call[0] == "init" {
			found = append(found, i)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one init, got %d:\n%s", len(found), strings.Join(f.commands(), "\n"))
	}
	input := f.inputs[found[0]]
	if input == nil {
		t.Fatalf("init was fed nothing on stdin: %s", strings.Join(f.calls[found[0]], " "))
	}
	var doc struct {
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(input, &doc); err != nil {
		t.Fatalf("init's stdin is not the document the CLI reads: %v\n%s", err, input)
	}
	return doc.Config
}

// carrier is one place a pack can hand the driver a value that ends up in the
// instance configuration, and the key Incus files it under.
type carrier struct {
	name string
	spec func(marker string) Spec
	key  string
}

// carriers covers every config key Start writes from a Spec field. The
// network-config is not a row: it is rendered from addresses the emulator
// allocates, so no client marker can reach it, and it is held by
// TestTheRoutedNetplanDeclaresTheLaunchAddress through the same decoder.
var carriers = []carrier{
	{
		name: "the user data",
		spec: func(m string) Spec {
			return Spec{Name: "srv", Image: "alpine:3.21",
				CloudInit: "#cloud-config\nchpasswd:\n  list: |\n    root:" + m + "\n"}
		},
		key: "cloud-init.user-data",
	},
	{
		name: "a label",
		spec: func(m string) Spec {
			return Spec{Name: "srv", Image: "alpine:3.21",
				Labels: map[string]string{LabelKey: "scaleway", "feint.tag": m}}
		},
		key: "user.feint.tag",
	},
	{
		name: "an environment variable",
		spec: func(m string) Spec {
			return Spec{Name: "srv", Image: "alpine:3.21", Env: []string{"TOKEN=" + m}}
		},
		key: "environment.TOKEN",
	},
}

// The boot shapes Start builds argv for. Each assembles a different argv, so
// each is a place a payload could be put back on it.
var bootShapes = []struct {
	name   string
	shape  func(Spec) Spec
	driver func(*fakeRuntime) *Incus
}{
	{"bare", func(s Spec) Spec { return s }, newFakeDriver},
	{"routed", func(s Spec) Spec { s.PublicAddresses = []string{"203.0.113.7"}; return s }, newFakeDriver},
	{"on a network", func(s Spec) Spec {
		s.Attachments = []Attachment{{Network: "fnt-abc"}}
		return s
	}, newFakeDriver},
	{"virtual machine", func(s Spec) Spec { return s }, func(f *fakeRuntime) *Incus {
		d := newFakeDriver(f)
		d.VM = true
		return d
	}},
}

const stdinMarker = "MARQUEUR-910-s3cr3t"

func TestNoInstanceConfigReachesArgv(t *testing.T) {
	for _, c := range carriers {
		for _, shape := range bootShapes {
			t.Run(c.name+"/"+shape.name, func(t *testing.T) {
				f := &fakeRuntime{}
				startScript(f)
				d := shape.driver(f)

				if _, err := d.Start(context.Background(), shape.shape(c.spec(stdinMarker))); err != nil {
					t.Fatalf("start: %v", err)
				}
				if got := f.matching(stdinMarker); len(got) != 0 {
					t.Errorf("%s reached argv, readable by any local user in /proc/<pid>/cmdline:\n%s",
						c.name, strings.Join(got, "\n"))
				}
				if got := f.matching(c.key); len(got) != 0 {
					t.Errorf("the key %s is still set on argv:\n%s", c.key, strings.Join(got, "\n"))
				}
			})
		}
	}
}

func TestTheInstanceConfigArrivesOnStdin(t *testing.T) {
	for _, c := range carriers {
		for _, shape := range bootShapes {
			t.Run(c.name+"/"+shape.name, func(t *testing.T) {
				f := &fakeRuntime{}
				startScript(f)
				d := shape.driver(f)

				spec := shape.shape(c.spec(stdinMarker))
				if _, err := d.Start(context.Background(), spec); err != nil {
					t.Fatalf("start: %v", err)
				}
				got := f.initConfig(t)[c.key]
				if !strings.Contains(got, stdinMarker) {
					t.Errorf("%s did not reach the instance: %s = %q", c.name, c.key, got)
				}
				if c.key == "cloud-init.user-data" && got != spec.CloudInit {
					t.Errorf("the user data was altered on the way:\n got %q\nwant %q", got, spec.CloudInit)
				}
				// The binding's own label rides the same road, or ownership
				// (mustOwnInstance reads user.feint.provider) breaks.
				if spec.Labels != nil && f.initConfig(t)["user."+LabelKey] != "scaleway" {
					t.Errorf("the provider label did not reach the instance: %v", f.initConfig(t))
				}
				// Only init is fed: every other command reads /dev/null.
				for i, input := range f.inputs {
					if input != nil && f.calls[i][0] != "init" {
						t.Errorf("a command other than init was fed stdin: %s", strings.Join(f.calls[i], " "))
					}
				}
			})
		}
	}
}

// A client document is opaque: whatever it holds must come back out of the
// stdin document byte for byte, and nothing in it may make the YAML reader on
// the other side refuse the whole create, or alter it.
//
// The oracle is the measurement, not yamlVerbatim: a test that asked the
// predicate under test would agree with any mutation of it. Measured on
// 2026-11-15 with gopkg.in/yaml.v2 v2.4.0, gopkg.in/yaml.v3 v3.0.1 and
// go.yaml.in/yaml/v4 v4.0.0-rc.3: a raw DEL, C1 control or U+FFFE/U+FFFF makes
// each of them refuse the document ("control characters are not allowed"), and
// a raw NEL is folded into a space. No YAML reader runs here, since this package
// takes no dependency, so the test holds the property those readers need: none
// of these runes is written raw.
func TestTheStdinDocumentCarriesNoCharacterYAMLRefuses(t *testing.T) {
	hostile := strings.Join([]string{
		"#cloud-config",
		"runcmd:",
		`  - echo "quoted" 'single' \backslash \u0041 #hash: colon {brace} [bracket]`,
		"\ttabbed\r",
		"del:\x7f c1:\u0080\u0085\u009f nonchar:\ufffe\uffff bom:\ufeff",
		"ls:\u2028 ps:\u2029 html:<>& accent:é emoji:\U0001F600 nul:\x00 esc:\x1b",
	}, "\n")
	doc, err := instanceConfig(map[string]string{"cloud-init.user-data": hostile})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	refused := map[rune]string{
		0x7f: "DEL, refused", 0x80: "a C1 control, refused", 0x9f: "a C1 control, refused",
		0xfffe: "a noncharacter, refused", 0xffff: "a noncharacter, refused",
		0x85: "NEL, folded into a space",
	}
	for r, why := range refused {
		if strings.ContainsRune(string(doc), r) {
			t.Errorf("the stdin document carries %U raw (%s by a YAML reader):\n%q", r, why, doc)
		}
	}
	var back struct {
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(doc, &back); err != nil {
		t.Fatalf("the document no longer decodes: %v\n%s", err, doc)
	}
	if back.Config["cloud-init.user-data"] != hostile {
		t.Errorf("the user data did not survive the document:\n got %q\nwant %q",
			back.Config["cloud-init.user-data"], hostile)
	}
}

// echoStdinEnv turns this test binary into a process that copies its stdin to
// its stdout: TestRunCLIFeedsTheProcessItsStdin runs itself as the child, so
// the proof depends on no binary of the host (it used to skip without cat, and
// a skipped proof measures nothing).
const echoStdinEnv = "FEINT_TEST_ECHO_STDIN"

// TestHelperEchoStdin is the child, and a no-op in an ordinary run.
func TestHelperEchoStdin(t *testing.T) {
	if os.Getenv(echoStdinEnv) != "1" {
		return
	}
	_, _ = io.Copy(os.Stdout, os.Stdin)
	os.Exit(0)
}

// The real binary is fed what the context carries. The seam tests above prove
// the driver hands the payload over, and this proves runCLI passes it on to the
// process rather than dropping it.
func TestRunCLIFeedsTheProcessItsStdin(t *testing.T) {
	t.Setenv(echoStdinEnv, "1")
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	child := "-test.run=^TestHelperEchoStdin$"

	want := []byte("config:\n  cloud-init.user-data: " + stdinMarker + "\n")
	out, err := runCLI(withStdin(context.Background(), want), self, 30*time.Second, child)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("the process did not read what the context carried:\n got %q\nwant %q", out, want)
	}

	// And a command fed nothing still reads /dev/null, not the operator's
	// terminal: incus reads stdin whenever it is not a terminal.
	out, err = runCLI(context.Background(), self, 30*time.Second, child)
	if err != nil {
		t.Fatalf("run without input: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("a command fed nothing read %q", out)
	}
}

// create then start, as two commands: a start that fails leaves an instance
// created and never booted, with the user data already in its config. It is
// removed, not resumed — resuming would be the already-exists path, which boots
// without the device keys the failed call set (TestAFailedStartLeavesNoHalfMadeInstance)
// — and the next poweron creates it again, fed the same user data. The cleanup
// and the retry put the payload on argv no more than the first attempt did.
func TestAFailedFirstBootIsRemovedAndTheRetryIsFedTheUserDataAgain(t *testing.T) {
	f := &fakeRuntime{}
	exists, started, starts := false, false, 0
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch args[0] {
		case "init":
			exists = true
		case "list":
			if exists && started {
				return []byte(`[{"name":"srv","status":"Running","state":{"network":{}}}]`), nil, true
			}
			if exists {
				return []byte(`[{"name":"srv","status":"Stopped","state":{"network":{}}}]`), nil, true
			}
			return []byte(`[]`), nil, true
		case "start":
			starts++
			if starts == 1 {
				return nil, errors.New("Failed to run: forkstart"), true
			}
			started = true
		case "delete":
			exists, started = false, false
		}
		return nil, nil, false
	}
	d := newFakeDriver(f)
	spec := carriers[0].spec(stdinMarker)

	if _, err := d.Start(context.Background(), spec); err == nil {
		t.Fatal("a refused start was reported as a boot")
	}
	if exists {
		t.Fatalf("the created, never-started instance was left behind, holding the user data:\n%s",
			strings.Join(f.commands(), "\n"))
	}

	if _, err := d.Start(context.Background(), spec); err != nil {
		t.Fatalf("the retry: %v\n%s", err, strings.Join(f.commands(), "\n"))
	}
	var fed int
	for i, call := range f.calls {
		if call[0] != "init" {
			continue
		}
		var doc struct {
			Config map[string]string `json:"config"`
		}
		if err := json.Unmarshal(f.inputs[i], &doc); err != nil || doc.Config["cloud-init.user-data"] != spec.CloudInit {
			t.Errorf("init #%d was not fed the user data (%v): %q", fed+1, err, f.inputs[i])
		}
		fed++
	}
	if fed != 2 {
		t.Errorf("expected the retry to create the instance again, got %d init:\n%s",
			fed, strings.Join(f.commands(), "\n"))
	}
	if got := f.matching(stdinMarker); len(got) != 0 {
		t.Errorf("the cleanup or the retry put the user data on argv:\n%s", strings.Join(got, "\n"))
	}
	// Nor on another command's stdin: the delete reads /dev/null.
	for i, call := range f.calls {
		if call[0] != "init" && f.inputs[i] != nil {
			t.Errorf("%q was fed stdin: %q", strings.Join(call, " "), f.inputs[i])
		}
	}
}

// The same cleanup when the failure IS the caller's cancellation: the context
// Start receives is the HTTP request's, and a client that hangs up mid-boot
// cancels it. The fake behaves as exec.CommandContext does, refusing to run a
// command whose context is already done, so a delete issued on the caller's
// context never happens and the instance stays, holding the user data.
func TestACancelledFirstBootIsStillRemoved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := &fakeRuntime{}
	exists := false
	var deletes []string
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		switch args[0] {
		case "init":
			exists = true
		case "list":
			return []byte(`[]`), nil, true
		case "start":
			cancel() // the client hung up while the machine was starting
			return nil, context.Canceled, true
		}
		return nil, nil, false
	}
	d := newFakeDriver(f)
	inner := d.runner
	d.runner = func(cctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "delete" {
			if err := cctx.Err(); err != nil {
				return nil, err
			}
			deletes = append(deletes, strings.Join(args, " "))
			exists = false
		}
		return inner(cctx, args...)
	}

	if _, err := d.Start(ctx, carriers[0].spec(stdinMarker)); err == nil {
		t.Fatal("a cancelled start was reported as a boot")
	}
	if exists || len(deletes) != 1 {
		t.Fatalf("the cancelled boot left its instance behind, user data in its config (deletes run: %v):\n%s",
			deletes, strings.Join(f.commands(), "\n"))
	}
}

// A key the CLI's YAML reader would not take for a key makes it refuse the
// whole document, so the boot is refused at the entry instead, before anything
// reaches the runtime. The limit is 1024 bytes once encoded (maxConfigKey).
//
// One row per question: the boundary itself, on each loop that writes a key,
// and a key that is short as written and long once escaped — a check that
// measured the raw key would let that one through and pass every other row.
func TestAConfigKeyAYAMLReaderWouldRefuseIsRefused(t *testing.T) {
	label := func(n int) Spec { // a label key of n bytes once encoded
		return Spec{Name: "srv", Image: "alpine:3.21",
			Labels: map[string]string{strings.Repeat("k", n-2-len("user.")): "v"}}
	}
	env := func(n int) Spec {
		return Spec{Name: "srv", Image: "alpine:3.21",
			Env: []string{strings.Repeat("K", n-2-len("environment.")) + "=v"}}
	}
	escaped := Spec{Name: "srv", Image: "alpine:3.21",
		Labels: map[string]string{strings.Repeat(string(rune(0x7f)), 200): "v"}}

	for name, row := range map[string]struct {
		spec   Spec
		refuse bool
	}{
		"a label key at the limit":                   {label(1024), false},
		"a label key one byte over":                  {label(1025), true},
		"an environment key at the limit":            {env(1024), false},
		"an environment key one byte over":           {env(1025), true},
		"a label key short as written, long escaped": {escaped, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRuntime{}
			startScript(f)
			d := newFakeDriver(f)

			_, err := d.Start(context.Background(), row.spec)
			if row.refuse {
				if err == nil {
					t.Fatalf("accepted a key the YAML reader refuses:\n%s", strings.Join(f.commands(), "\n"))
				}
				if len(f.calls) != 0 {
					t.Errorf("refused after reaching the runtime:\n%s", strings.Join(f.commands(), "\n"))
				}
				return
			}
			if err != nil {
				t.Fatalf("refused a key the YAML reader accepts: %v", err)
			}
			if len(f.initConfig(t)) == 0 {
				t.Errorf("the instance was created with no configuration")
			}
		})
	}
}

// An environment entry that is not KEY=VALUE was refused by the CLI when it
// rode argv; on stdin it would silently become an empty variable, or one named
// "environment.". Refused before anything is created, and without quoting the
// entry, which may be a secret.
//
// One row per half of the condition, each failing only that half: the entry
// with no `=` has a key, and the entry with an empty key has its `=`. The
// accepting half is the "an environment variable" carrier above.
func TestAnEnvironmentEntryThatIsNotKeyValueIsRefused(t *testing.T) {
	for name, entry := range map[string]string{
		"no equals sign": stdinMarker,
		"an empty key":   "=" + stdinMarker,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRuntime{}
			startScript(f)
			d := newFakeDriver(f)

			_, err := d.Start(context.Background(), Spec{Name: "srv", Image: "alpine:3.21",
				Env: []string{"GOOD=1", entry}})
			if err == nil {
				t.Fatalf("%q was accepted:\n%s", entry, strings.Join(f.commands(), "\n"))
			}
			if strings.Contains(err.Error(), stdinMarker) {
				t.Errorf("the refusal quotes the entry, which may be a secret: %v", err)
			}
			// Nothing at all, the default network included: the refusal
			// sits with the other guards at the entry of Start.
			if len(f.calls) != 0 {
				t.Errorf("refused after reaching the runtime:\n%s", strings.Join(f.commands(), "\n"))
			}
		})
	}
}

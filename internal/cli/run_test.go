package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/scaleway"
	"github.com/stephrobert/feint/internal/s3"
)

// reportServer serves one validated S3 report over loopback, which is what
// fetchS3 requires and therefore what `feint run` discovers its doors from.
func reportServer(t *testing.T, doors ...s3.Door) *httptest.Server {
	t.Helper()
	report := s3.Report{SchemaVersion: s3.ReportVersion, Enabled: len(doors) != 0,
		MaxBlobBytes: 1 << 20, MaxObjectBytes: 1 << 10,
		Doors: doors, HTTPRequests: map[string]uint64{}}
	if report.Doors == nil {
		report.Doors = []s3.Door{}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		if err := json.NewEncoder(w).Encode(report); err != nil {
			t.Errorf("encode report: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func door(provider, region string) s3.Door {
	return s3.Door{Provider: provider, Region: region, Path: s3.DoorPath(provider, region)}
}

// TestRunDiscoversItsDoorsAndNeverTakesAHostnameFromTheOperator is the
// accepting half: a mounted door becomes an interception whose target is the
// door's own canonical path, and nothing in that mapping was typed.
func TestRunDiscoversItsDoorsAndNeverTakesAHostnameFromTheOperator(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"), door("exoscale", "ch-gva-2"))
	profiles, err := runProfiles(server.URL)
	if err != nil {
		t.Fatalf("mounted doors refused: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	want := map[string]string{
		"s3.fr-par.scw.cloud": "scaleway/fr-par",
		"sos-ch-gva-2.exo.io": "exoscale/ch-gva-2",
	}
	for _, profile := range profiles {
		scope, ok := want[profile.CloudHost]
		if !ok || scope != profile.Provider+"/"+profile.Region {
			t.Fatalf("authority %q is not the one the pack declares for %s/%s",
				profile.CloudHost, profile.Provider, profile.Region)
		}
	}
	// The transport the command builds must accept exactly these, which is the
	// property that makes an off-loopback target unspellable: the only targets
	// are doors the emulator reported.
	if _, err := s3.NewLocalTransport(server.URL, profiles); err != nil {
		t.Fatalf("discovered profiles rejected by the local transport: %v", err)
	}
}

// TestRunRefusesAnEmulatorWithNoDoorAndSaysWhatToStart keeps the refusal
// actionable: an operator who forgot --objectstore reads what to add, not that
// something is unavailable.
func TestRunRefusesAnEmulatorWithNoDoorAndSaysWhatToStart(t *testing.T) {
	server := reportServer(t)
	_, err := runProfiles(server.URL)
	if err == nil {
		t.Fatal("a doorless emulator was accepted")
	}
	for _, mention := range []string{"--objectstore", "--s3-doors"} {
		if !strings.Contains(err.Error(), mention) {
			t.Fatalf("the refusal does not name %s: %v", mention, err)
		}
	}
}

// authorityLessPack is a Scaleway pack that declares no measured cloud
// authority, which is the one case runProfiles must refuse rather than guess.
type authorityLessPack struct{ emulator.Pack }

func (authorityLessPack) ObjectStorage(region string) s3.Profile {
	return s3.Profile{Provider: "scaleway", Region: region}
}

// TestRunRefusesADoorWithNoMeasuredAuthority is cited by runProfiles. Without
// its check, the command would hand s3.NewLocalTransport an empty authority and
// fail with a message about transports rather than about the door.
func TestRunRefusesADoorWithNoMeasuredAuthority(t *testing.T) {
	previous := packsFor
	packsFor = func(env *emulator.Env) ([]emulator.Pack, error) {
		return []emulator.Pack{authorityLessPack{Pack: scaleway.New(env)}}, nil
	}
	t.Cleanup(func() { packsFor = previous })

	server := reportServer(t, door("scaleway", "fr-par"))
	_, err := runProfiles(server.URL)
	if err == nil {
		t.Fatal("a door with no measured authority was accepted")
	}
	if !strings.Contains(err.Error(), "scaleway/fr-par") {
		t.Fatalf("the refusal does not name the door: %v", err)
	}
}

// TestRunRefusesCredentialsThatAreNotKnownFakesByNameOnly holds both halves of
// the fail-closed rule, and the one that matters most: the NAME is reported and
// the value never is, because this output lands in terminals and bug reports.
func TestRunRefusesCredentialsThatAreNotKnownFakesByNameOnly(t *testing.T) {
	const secret = "AKIAREALLOOKINGKEY42"
	offenders := realCredentials([]string{
		"PATH=/usr/bin",
		"SCW_ACCESS_KEY=" + knownFakes[2],
		"AWS_SECRET_ACCESS_KEY=",
		"OSC_ACCESS_KEY=" + secret,
	})
	if len(offenders) != 1 || offenders[0] != "OSC_ACCESS_KEY" {
		t.Fatalf("got %v, want only OSC_ACCESS_KEY", offenders)
	}
	if none := realCredentials([]string{"SCW_SECRET_KEY=" + knownFakes[3]}); len(none) != 0 {
		t.Fatalf("a known fake was refused: %v", none)
	}

	var stdout, stderr bytes.Buffer
	t.Setenv("OSC_ACCESS_KEY", secret)
	if code := runCommand([]string{"--", "true"}, &stdout, &stderr); code != exitError {
		t.Fatalf("the run was not refused: code %d", code)
	}
	if strings.Contains(stderr.String(), secret) || strings.Contains(stdout.String(), secret) {
		t.Fatal("the credential value was printed")
	}
	if !strings.Contains(stderr.String(), "OSC_ACCESS_KEY") {
		t.Fatalf("the refusal does not name the variable: %q", stderr.String())
	}
}

// TestRunRemovesItsAuthorityAfterASignal is cited by runChild. The child sends
// SIGTERM to this test process, which is the real path: runChild arms
// signal.Notify before starting the child, so the signal is relayed rather than
// killing the test, and the deferred cleanup is what this asserts.
//
// Which half catches which, because getting this backwards is how a test comes
// to be believed for the wrong reason: the deferred removal would run even
// without the relay, so the file check does NOT hold the relay. What holds it
// is the exit code — a child that never receives the signal sleeps out and
// reports success. The file check holds the cleanup, including on this path.
func TestRunRemovesItsAuthorityAfterASignal(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"))
	profiles, err := runProfiles(server.URL)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	// `kill -TERM $PPID` targets this test process; runChild relays it to the
	// child, which then dies on it rather than sleeping out the timeout.
	code, err := runChild(server.URL, "127.0.0.1:0", profiles,
		[]string{"sh", "-c", "kill -TERM $PPID; sleep 30"}, false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if code == 0 {
		t.Fatal("the child reported success after being terminated")
	}

	// The receipt names the authority, which is how its removal is observable
	// at all — the directory is private to the run.
	var authority string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if _, after, ok := strings.Cut(line, "authority at "); ok {
			authority = strings.TrimSpace(after)
		}
	}
	if authority == "" {
		t.Fatalf("the receipt does not name the authority: %q", stdout.String())
	}
	if _, err := os.Stat(authority); !os.IsNotExist(err) {
		t.Fatalf("the authority survived the signal at %s: %v", authority, err)
	}
}

// TestRunKeepsItsAuthorityOwnerOnly is cited by runChild, and it asks the
// question from where it matters: inside the child, through the variable the
// child was actually given. A test that stat'd a path this test computed would
// be checking its own arithmetic.
func TestRunKeepsItsAuthorityOwnerOnly(t *testing.T) {
	server := reportServer(t, door("scaleway", "fr-par"))
	profiles, err := runProfiles(server.URL)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code, err := runChild(server.URL, "127.0.0.1:0", profiles,
		[]string{"sh", "-c", `stat -c '%a %n' "$SSL_CERT_FILE" "$(dirname "$SSL_CERT_FILE")"`},
		false, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("the child could not read its own authority: code %d, %v, %q", code, err, stderr.String())
	}
	// Two properties, and they pull in opposite directions, which is why both
	// are asserted: the directory must be owner-only, and the CA inside it must
	// still be READABLE by the client process. proxy.WriteCA writes 0644 for
	// that second reason, deliberately; the privacy comes from the directory,
	// and the authority's private key is never written at all.
	if !strings.Contains(stdout.String(), "700 ") {
		t.Fatalf("the child saw a directory mode other than 700: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ca.pem") {
		t.Fatalf("the child could not name its own authority: %q", stdout.String())
	}
}

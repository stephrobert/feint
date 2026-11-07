package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/s3"
)

func privateStorageDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStartPreservesObjectStorageFlagsAndAbsoluteRoot(t *testing.T) {
	fs := newFlagSet("start")
	flags := bindServeFlags(fs)
	if err := fs.Parse([]string{"--objectstore", "relative-bytes", "--s3-doors", "scaleway/fr-par",
		"--objectstore-budget-mib", "3", "--s3-object-limit-mib", "1", "--s3-host-doors"}); err != nil {
		t.Fatal(err)
	}
	if err := flags.storage.validate(); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(flags.storage.root) {
		t.Fatal("a restart from another directory would select another object store")
	}
	replaySet := newFlagSet("start")
	replay := bindServeFlags(replaySet)
	if err := replaySet.Parse(flags.args()[1:]); err != nil {
		t.Fatal(err)
	}
	if *replay.storage != *flags.storage {
		t.Fatalf("restart loses storage settings: %+v versus %+v", replay.storage, flags.storage)
	}
}

func TestObjectStorageValidatesDefaultsAndLimitsWithoutStartingAnything(t *testing.T) {
	fs := newFlagSet("serve")
	defaults := bindObjectStorageFlags(fs)
	if err := defaults.validate(); err != nil || defaults.budgetMiB<<20 != 1<<30 || defaults.objectMiB<<20 != 256<<20 {
		t.Fatalf("approved defaults moved: %+v %v", defaults, err)
	}
	for _, limits := range [][2]int64{{0, 1}, {-1, 1}, {1, 0}, {1, 2}, {1 << 44, 1}, {1 << 42, 1 << 42}} {
		flags := objectStorageFlags{budgetMiB: limits[0], objectMiB: limits[1]}
		if err := flags.validate(); err == nil {
			t.Fatalf("invalid/overflowing limits accepted: %v", limits)
		}
	}
	if len(defaults.args()) != 0 {
		t.Fatal("disabled byte storage rendered activation flags")
	}
	defaults.hosts = true
	if err := defaults.validate(); err == nil {
		t.Fatal("host doors silently activated without byte storage")
	}
}

func TestS3ListenConfigurationAlwaysProducesUsableLoopbackEndpoints(t *testing.T) {
	for _, tc := range []struct{ addr, endpoint string }{
		{"127.0.0.1:4599", "http://127.0.0.1:4599"},
		{"localhost:4599", "http://localhost:4599"},
		{":4599", "http://127.0.0.1:4599"},
		{"0.0.0.0:4599", "http://127.0.0.1:4599"},
		{"[::]:4599", "http://[::1]:4599"},
		{"[::1]:4599", "http://[::1]:4599"},
	} {
		if got, err := storageListenEndpoint(tc.addr); err != nil || got != tc.endpoint {
			t.Fatalf("%s advertised an unusable endpoint: %q %v", tc.addr, got, err)
		}
	}
	for _, addr := range []string{"192.0.2.10:4599", "cloud.example:4599", "invalid-address"} {
		if _, err := storageListenEndpoint(addr); err == nil {
			t.Fatalf("S3 accepted a listener that cannot receive loopback peers: %s", addr)
		}
	}
}

func storageServer(t *testing.T, enabled bool, declaration string) (*httptest.Server, *s3.Doors) {
	t.Helper()
	srv, env, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	flags := &objectStorageFlags{budgetMiB: 3, objectMiB: 1, doors: declaration}
	if enabled {
		flags.root = filepath.Join(privateStorageDir(t), "bytes")
	}
	doors, store, err := mountObjectStorage(t.Context(), srv, flags, env.Log)
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	ts := httptest.NewServer(doors)
	t.Cleanup(ts.Close)
	return ts, doors
}

func TestS3DeclarationsRejectUnknownMalformedAndDuplicateDoors(t *testing.T) {
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "scaleway", "scaleway/fr-par,scaleway/fr-par", "outscale/eu-west-2",
		"unknown/fr-par", "scaleway/FR-PAR", "scaleway/fr-par/extra", "scaleway/../fr-par"} {
		if _, err := storageProfiles(srv.Packs(), value); err == nil {
			t.Fatalf("invalid or unsupported declaration accepted: %q", value)
		}
	}
	profiles, err := storageProfiles(srv.Packs(), defaultS3Doors)
	if err != nil || len(profiles) != 2 {
		t.Fatalf("approved explicit doors rejected: %+v %v", profiles, err)
	}
}

func TestS3EnvOnlyExportsLiveSelectedDoorsAndUnsetWorksOffline(t *testing.T) {
	ts, _ := storageServer(t, true, defaultS3Doors+",scaleway/nl-ams")
	var out, note strings.Builder
	call := func(args ...string) int {
		out.Reset()
		note.Reset()
		return envCommand(args, &out, &note)
	}
	if code := call("scaleway", "--s3", "--endpoint", ts.URL); code != 1 || out.Len() != 0 {
		t.Fatalf("ambiguous regions printed a success-shaped environment: %d %q", code, out.String())
	}
	for _, shell := range []string{"bash", "fish", "powershell"} {
		if code := call("scaleway", "--s3", "--s3-region", "fr-par", "--endpoint", ts.URL, "--shell", shell); code != 0 ||
			!strings.Contains(out.String(), ts.URL+strings.TrimSuffix(s3.DoorPath("scaleway", "fr-par"), "/")) ||
			strings.Contains(out.String(), ts.URL+s3.DoorPath("scaleway", "fr-par")) ||
			strings.Contains(out.String(), "not certified") || !strings.Contains(note.String(), "not certified") {
			t.Fatalf("live selected S3 environment or stderr contract failed: %d %q %q", code, out.String(), note.String())
		}
	}
	if code := call("exoscale", "--s3", "--endpoint", ts.URL); code != 0 ||
		!strings.Contains(out.String(), "EXOSCALE_SOS_ENDPOINT") || !strings.Contains(out.String(), "EXOSCALE_STORAGE_API_ENDPOINT") {
		t.Fatalf("SOS configurable endpoint names were not wired: %d %q", code, out.String())
	}
	if code := call("scaleway", "--s3", "--s3-region", "missing", "--endpoint", ts.URL); code != 1 || out.Len() != 0 {
		t.Fatal("an undeclared region exported a fallback endpoint")
	}
	if code := call("scaleway", "--s3-region", "fr-par"); code != 1 || out.Len() != 0 {
		t.Fatal("a region without S3 activation was silently ignored")
	}
	if code := call("scaleway", "--s3", "--unset", "--endpoint", "http://127.0.0.1:1"); code != 0 ||
		!strings.Contains(out.String(), "unset SCW_S3_ENDPOINT") {
		t.Fatal("unset requires a running emulator or forgets the S3 variable")
	}
	if code := call("scaleway", "--unset"); code != 0 || !strings.Contains(out.String(), "unset SCW_S3_ENDPOINT") {
		t.Fatal("ordinary unset leaves the new S3 endpoint behind")
	}
	if code := call("scaleway", "--endpoint", ts.URL); code != 0 || strings.Contains(out.String(), "SCW_S3_ENDPOINT") {
		t.Fatal("ordinary offline-compatible env unexpectedly activates S3")
	}
}

func TestS3DisabledStateIsHonestInEnvStatusAndDoctor(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	ts, _ := storageServer(t, false, "")
	var out, note strings.Builder
	if code := envCommand([]string{"scaleway", "--s3", "--endpoint", ts.URL}, &out, &note); code != 1 ||
		out.Len() != 0 || !strings.Contains(note.String(), "disabled") {
		t.Fatalf("disabled S3 emitted an environment: %d %q %q", code, out.String(), note.String())
	}
	addr := strings.TrimPrefix(ts.URL, "http://")
	if c := checkObjectStorage(addr); c.state != verdictFail || !strings.Contains(c.detail, "disabled") {
		t.Fatalf("doctor claims disabled bytes are available: %+v", c)
	}
	out.Reset()
	note.Reset()
	if code := status([]string{"--addr", addr, "--format", "json"}, &out, &note); code != 0 {
		t.Fatal("status failed:", note.String())
	}
	var report statusReport
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil || !report.Running ||
		report.ObjectStorage == nil || report.ObjectStorage.Enabled || report.ObjectStorageError != "" {
		t.Fatalf("status loses disabled activation: %+v %v", report, err)
	}
}

func TestS3DiscoveryRefusesRedirectsInvalidReportsAndForeignEndpoints(t *testing.T) {
	var destinationHits int
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationHits++
		_, _ = io.WriteString(w, "unexpected")
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()
	if _, err := fetchS3(source.URL); err == nil || destinationHits != 0 {
		t.Fatal("discovery followed a redirect")
	}
	for _, endpoint := range []string{"https://s3.fr-par.scw.cloud", "http://127.0.0.1:1/extra",
		"http://user:password@127.0.0.1:1", "http://127.0.0.1:1?q=1", "http://127.0.0.1:1#fragment"} {
		if _, err := s3DiscoveryURL(endpoint); err == nil {
			t.Fatalf("unsafe discovery endpoint accepted: %q", endpoint)
		}
	}
	if target, err := s3DiscoveryURL(source.URL + "/"); err != nil || target.Path != s3.ReportPath {
		t.Fatalf("valid loopback discovery URL refused: %v %v", target, err)
	}
	valid := s3.Report{SchemaVersion: s3.ReportVersion, Enabled: true, MaxBlobBytes: 3, MaxObjectBytes: 1,
		Doors:        []s3.Door{{Provider: "scaleway", Region: "fr-par", Path: s3.DoorPath("scaleway", "fr-par")}},
		HTTPRequests: map[string]uint64{}}
	for _, modify := range []func(*s3.Report){
		func(r *s3.Report) { r.SchemaVersion++ },
		func(r *s3.Report) { r.MaxObjectBytes = 4 },
		func(r *s3.Report) { r.Enabled = false },
		func(r *s3.Report) { r.Doors[0].Path = "/foreign" },
		func(r *s3.Report) { r.Doors[0].Host = "cloud.example" },
		func(r *s3.Report) { r.Doors[0].Host = s3.DoorHost("scaleway", "nl-ams") },
		func(r *s3.Report) { r.Doors = append(r.Doors, r.Doors[0]) },
	} {
		report := valid
		report.Doors = append([]s3.Door{}, valid.Doors...)
		modify(&report)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(report)
		}))
		if _, err := fetchS3(server.URL); err == nil {
			server.Close()
			t.Fatalf("invalid report trusted: %+v", report)
		}
		server.Close()
	}
}

func TestS3HostEnvironmentUsesOnlyDeclaredAuthorityAndLocalProxy(t *testing.T) {
	srv, env, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	flags := &objectStorageFlags{root: filepath.Join(privateStorageDir(t), "bytes"),
		doors: defaultS3Doors, budgetMiB: 3, objectMiB: 1, hosts: true}
	doors, store, err := mountObjectStorage(t.Context(), srv, flags, env.Log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	ts := httptest.NewServer(doors.Handler("127.0.0.1:4599"))
	defer ts.Close()
	for _, p := range srv.Packs() {
		if p.Name() != "exoscale" {
			continue
		}
		pack, ok := p.(packEnv)
		if !ok {
			t.Fatal("Exoscale environment missing")
		}
		vars := pack.Env(ts.URL).Vars
		if err := storageEnvironment(pack, ts.URL, "ch-gva-2", false, vars); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
			if vars[key] != ts.URL {
				t.Fatalf("%s did not select the literal local proxy: %q", key, vars[key])
			}
		}
		for _, key := range []string{"NO_PROXY", "no_proxy"} {
			if vars[key] != "127.0.0.1,localhost,::1" {
				t.Fatalf("%s could bypass proxy interception: %q", key, vars[key])
			}
		}
	}
	for _, shell := range []string{"bash", "fish", "powershell"} {
		var out, note strings.Builder
		code := envCommand([]string{"exoscale", "--s3", "--endpoint", ts.URL, "--shell", shell}, &out, &note)
		if code != 0 || !strings.Contains(out.String(), "http://"+s3.DoorHost("exoscale", "ch-gva-2")) ||
			!strings.Contains(out.String(), "HTTP_PROXY") || !strings.Contains(out.String(), "http_proxy") ||
			!strings.Contains(out.String(), ts.URL) || !strings.Contains(out.String(), "127.0.0.1,localhost,::1") ||
			!strings.Contains(note.String(), "proxy settings") {
			t.Fatalf("host endpoint/proxy environment missing: %d %q %q", code, out.String(), note.String())
		}
	}
	report, err := fetchS3(ts.URL)
	if err != nil || report.Doors[1].Host != s3.DoorHost("exoscale", "ch-gva-2") {
		t.Fatal("live discovery lost the declared host:", report, err)
	}
	var scaleway, scalewayNote strings.Builder
	if code := envCommand([]string{"scaleway", "--s3", "--endpoint", ts.URL}, &scaleway, &scalewayNote); code != 0 ||
		!strings.Contains(scaleway.String(), ts.URL+strings.TrimSuffix(s3.DoorPath("scaleway", "fr-par"), "/")) ||
		strings.Contains(scaleway.String(), "HTTP_PROXY") {
		t.Fatal("host activation broke the measured Scaleway prefix environment:", code, scaleway.String())
	}
	if c := checkObjectStorage(strings.TrimPrefix(ts.URL, "http://")); c.state != verdictOK ||
		!strings.Contains(c.detail, report.Doors[1].Host) {
		t.Fatal("doctor did not describe the required local proxy:", c)
	}
	var out, note strings.Builder
	if code := envCommand([]string{"exoscale", "--s3", "--unset"}, &out, &note); code != 0 ||
		!strings.Contains(out.String(), "unset HTTP_PROXY") || !strings.Contains(out.String(), "unset no_proxy") {
		t.Fatal("explicit S3 unset did not remove local proxy settings:", code, out.String())
	}
	out.Reset()
	if code := envCommand([]string{"exoscale", "--unset"}, &out, &note); code != 0 || strings.Contains(out.String(), "HTTP_PROXY") {
		t.Fatal("ordinary unset changed unrelated proxy settings:", code, out.String())
	}
	out.Reset()
	if code := envCommand([]string{"scaleway", "--s3", "--unset"}, &out, &note); code != 0 || strings.Contains(out.String(), "HTTP_PROXY") {
		t.Fatal("prefix-only Scaleway unset changed unrelated proxy settings:", code, out.String())
	}
}

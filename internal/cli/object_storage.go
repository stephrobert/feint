package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/s3"
)

// defaultS3Doors is what a reader gets with --objectstore and no door list.
//
// TWO doors, one per provider that has a recorded dialect, and the count is the
// constraint rather than the content: a second Exoscale region here makes
// `feint env exoscale --s3` ambiguous, so it answers "multiple S3 regions are
// enabled; select one with --s3-region" to somebody who asked for nothing
// unusual. Measured by adding exoscale/ch-dk-2 and watching three tests of the
// env surface fail at once.
//
// The conformance starters arm their own wider list, because they drive a stack
// in another zone; see tools/conformance's
// TestEveryConformanceEmulatorArmsTheSameObjectStore for what keeps the three
// of them in step.
const defaultS3Doors = "scaleway/fr-par,exoscale/ch-gva-2"

type objectStorageFlags struct {
	root      string
	doors     string
	budgetMiB int64
	objectMiB int64
	hosts     bool
}

func bindObjectStorageFlags(fs *flag.FlagSet) *objectStorageFlags {
	f := &objectStorageFlags{}
	fs.StringVar(&f.root, "objectstore", "", "opt in to persistent S3 bytes in this private directory (independent of --state)")
	fs.StringVar(&f.doors, "s3-doors", defaultS3Doors, "comma-separated provider/region S3 doors; only these prefixes are served")
	fs.BoolVar(&f.hosts, "s3-host-doors", false, "also accept declared synthetic S3 hosts through local HTTP proxy requests on this same port")
	fs.Int64Var(&f.budgetMiB, "objectstore-budget-mib", 1024, "cumulative retained and reserved blob budget in MiB; deletion does not free blobs")
	fs.Int64Var(&f.objectMiB, "s3-object-limit-mib", 256, "maximum decoded simple PUT size in MiB")
	return f
}

func (f *objectStorageFlags) validate() error {
	if f.hosts && f.root == "" {
		return errors.New("--s3-host-doors requires --objectstore")
	}
	if f.budgetMiB <= 0 || f.budgetMiB > math.MaxInt64>>20 ||
		f.objectMiB <= 0 || f.objectMiB > (math.MaxInt64-(64<<10))/2>>20 ||
		f.objectMiB > f.budgetMiB {
		return errors.New("object storage requires positive representable MiB limits, with the object limit no larger than the blob budget")
	}
	if f.root != "" {
		absolute, err := filepath.Abs(f.root)
		if err != nil {
			return fmt.Errorf("object storage root: %w", err)
		}
		f.root = absolute
	}
	return nil
}

func (f *objectStorageFlags) args() []string {
	if f.root == "" {
		return nil
	}
	args := []string{"--objectstore", f.root, "--s3-doors", f.doors,
		"--objectstore-budget-mib", fmt.Sprint(f.budgetMiB), "--s3-object-limit-mib", fmt.Sprint(f.objectMiB)}
	if f.hosts {
		args = append(args, "--s3-host-doors")
	}
	return args
}

type packObjectStorage interface {
	ObjectStorage(region string) s3.Profile
}

func storageProfiles(packs []emulator.Pack, declaration string) ([]s3.Profile, error) {
	var profiles []s3.Profile
	seen := make(map[string]bool)
	for _, entry := range strings.Split(declaration, ",") {
		provider, region, ok := strings.Cut(entry, "/")
		if !ok || !s3.ValidToken(provider) || !s3.ValidToken(region) || seen[entry] {
			return nil, fmt.Errorf("invalid or duplicate S3 door %q; use provider/region tokens", entry)
		}
		seen[entry] = true
		var found bool
		for _, p := range packs {
			if p.Name() != provider {
				continue
			}
			if storage, ok := p.(packObjectStorage); ok {
				profiles = append(profiles, storage.ObjectStorage(region))
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("provider %q declares no supported S3 dialect", provider)
		}
	}
	return profiles, nil
}

func mountObjectStorage(ctx context.Context, srv *emulator.Server, flags *objectStorageFlags, log *slog.Logger) (*s3.Doors, *objectstore.Store, error) {
	if flags.root == "" {
		doors, err := s3.Mount(srv.Handler(), nil, nil, 0, log)
		return doors, nil, err
	}
	profiles, err := storageProfiles(srv.Packs(), flags.doors)
	if err != nil {
		return nil, nil, err
	}
	store, err := objectstore.OpenWithLimit(ctx, flags.root, flags.budgetMiB<<20)
	if err != nil {
		return nil, nil, fmt.Errorf("open object storage: %w", err)
	}
	mount := s3.Mount
	if flags.hosts {
		mount = s3.MountHosts
	}
	doors, err := mount(srv.Handler(), store, profiles, flags.objectMiB<<20, log)
	if err != nil {
		return nil, nil, errors.Join(err, store.Close())
	}
	return doors, store, nil
}

func s3DiscoveryURL(endpoint string) (*url.URL, error) {
	base, err := url.Parse(endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") ||
		!emulator.LoopbackListen(net.JoinHostPort(base.Hostname(), "0")) || base.User != nil || base.RawQuery != "" ||
		base.Fragment != "" || base.RawPath != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("S3 discovery requires an explicit loopback HTTP(S) control-plane endpoint without credentials, query or path")
	}
	base.Path = s3.ReportPath
	return base, nil
}

func storageListenEndpoint(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("S3 listen address: %w", err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	local := net.JoinHostPort(host, port)
	// TestS3ListenConfigurationAlwaysProducesUsableLoopbackEndpoints holds this.
	if !emulator.LoopbackListen(local) {
		return "", errors.New("S3 requires a loopback or wildcard listener; a specific non-loopback listen address cannot serve its loopback-only doors")
	}
	return "http://" + local, nil
}

func fetchS3(endpoint string) (report s3.Report, result error) {
	base, err := s3DiscoveryURL(endpoint)
	if err != nil {
		return report, err
	}
	// Discovery must not follow a redirect to a cloud or use an inherited proxy.
	client := &http.Client{Timeout: statusTimeout, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base.String(), nil)
	if err != nil {
		return report, err
	}
	res, err := client.Do(req)
	if err != nil {
		return report, err
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close S3 report: %w", err))
		}
	}()
	if res.StatusCode != http.StatusOK {
		return report, fmt.Errorf("S3 discovery answered %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	if err != nil {
		return report, fmt.Errorf("read S3 report: %w", err)
	}
	if len(raw) > 64<<10 {
		return report, errors.New("S3 report exceeds 64 KiB")
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return report, fmt.Errorf("decode S3 report: %w", err)
	}
	if report.SchemaVersion != s3.ReportVersion || report.Doors == nil || report.HTTPRequests == nil ||
		(report.Enabled && (report.MaxBlobBytes <= 0 || report.MaxObjectBytes <= 0 ||
			report.MaxObjectBytes > report.MaxBlobBytes || len(report.Doors) == 0)) ||
		(!report.Enabled && len(report.Doors) != 0) {
		return report, errors.New("S3 report has an unsupported version or inconsistent activation/limits")
	}
	seen := make(map[string]bool)
	for _, door := range report.Doors {
		if !s3.ValidToken(door.Provider) || !s3.ValidToken(door.Region) ||
			door.Path != s3.DoorPath(door.Provider, door.Region) || seen[door.Path] ||
			(door.Host != "" && door.Host != s3.DoorHost(door.Provider, door.Region)) {
			return report, errors.New("S3 report declares a noncanonical or duplicate endpoint")
		}
		seen[door.Path] = true
	}
	return report, nil
}

func storageEnvironment(pack packEnv, endpoint, region string, unset bool, vars map[string]string) error {
	storage, ok := pack.(packObjectStorage)
	if !ok {
		return fmt.Errorf("the %s pack declares no S3 environment", pack.Name())
	}
	if unset {
		profile := storage.ObjectStorage(region)
		for _, key := range profile.Variables {
			vars[key] = ""
		}
		return nil
	}
	report, err := fetchS3(endpoint)
	if err != nil {
		return err
	}
	if !report.Enabled {
		return errors.New("S3 is disabled; start with --objectstore before exporting an S3 environment")
	}
	var selected *s3.Door
	for _, door := range report.Doors {
		if door.Provider == pack.Name() && (region == "" || door.Region == region) {
			if selected != nil {
				return errors.New("multiple S3 regions are enabled; select one with --s3-region")
			}
			copy := door
			selected = &copy
		}
	}
	if selected == nil {
		return fmt.Errorf("no enabled S3 door for provider %q and selected region %q", pack.Name(), region)
	}
	profile := storage.ObjectStorage(selected.Region)
	target := strings.TrimSuffix(endpoint, "/") + strings.TrimSuffix(selected.Path, "/")
	if profile.HostEndpoint && selected.Host != "" {
		if !strings.HasPrefix(endpoint, "http://") {
			return errors.New("S3 host doors require a local HTTP proxy endpoint, not HTTPS")
		}
		target = "http://" + selected.Host
		for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
			vars[key] = strings.TrimSuffix(endpoint, "/")
		}
		for _, key := range []string{"NO_PROXY", "no_proxy"} {
			vars[key] = "127.0.0.1,localhost,::1"
		}
	}
	for _, key := range profile.Variables {
		vars[key] = target
	}
	vars[profile.RegionVariable] = selected.Region
	return nil
}

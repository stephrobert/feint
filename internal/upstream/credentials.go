package upstream

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// credentials are read at the moment of use and never stored anywhere.
//
// They come from the files the official clients already use, so an operator who
// can run `scw`, `oapi-cli` or `exo` can run this with no extra setup — and,
// more importantly, so this package never becomes a second place where a cloud
// credential lives.
type credentials struct {
	key      string
	secret   string
	region   string
	endpoint string
}

// loadCredentials reads one profile for one provider.
//
// An unknown or unusable profile is an error here, at the one point where
// something can be said about it, rather than a client that answers 401 thirty
// times and leaves a reader to guess which of the two ends is wrong.
func loadCredentials(p Provider, profile string) (credentials, error) {
	switch p {
	case Scaleway:
		return scalewayCredentials(profile)
	case Outscale:
		return outscaleCredentials(profile)
	case Exoscale:
		return exoscaleCredentials(profile)
	default:
		return credentials{}, fmt.Errorf("unknown provider %q", p)
	}
}

// scalewayCredentials reads ~/.config/scw/config.yaml.
//
// Parsed with a line matcher rather than a YAML library: the file's shape is
// three flat keys, and rule 6 of this project asks that a dependency be earned.
// A profile is a nested block, so the search is scoped to it when one is named.
func scalewayCredentials(profile string) (credentials, error) {
	path := filepath.Join(configHome(), "scw", "config.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // the operator's own client config
	if err != nil {
		return credentials{}, fmt.Errorf("scaleway: %w", err)
	}
	text := string(raw)
	if profile != "" {
		var found bool
		text, found = scalewayProfile(text, profile)
		if !found {
			return credentials{}, fmt.Errorf("scaleway: no profile %q in %s", profile, path)
		}
	}
	c := credentials{
		key:      yamlValue(text, "access_key"),
		secret:   yamlValue(text, "secret_key"),
		region:   "fr-par",
		endpoint: "https://api.scaleway.com",
	}
	if c.secret == "" {
		return credentials{}, fmt.Errorf("scaleway: no secret_key in %s", path)
	}
	return c, nil
}

// scalewayProfile returns the block of one named profile.
func scalewayProfile(text, name string) (string, bool) {
	marker := regexp.MustCompile(`(?m)^\s{2}` + regexp.QuoteMeta(name) + `:\s*$`)
	loc := marker.FindStringIndex(text)
	if loc == nil {
		return "", false
	}
	rest := text[loc[1]:]
	// The block ends at the next line indented by exactly two spaces, which is
	// where the sibling profile begins.
	if next := regexp.MustCompile(`(?m)^\s{2}\S`).FindStringIndex(rest); next != nil {
		rest = rest[:next[0]]
	}
	return rest, true
}

func yamlValue(text, key string) string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `:\s*"?([^"\s]+)"?\s*$`)
	if m := re.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// outscaleCredentials reads ~/.osc/config.json.
//
// The region comes from `region_name` or `region`, in that order, and the
// distinction matters: `region_name` is osc-cli's key and `region` is
// oapi-cli's, the same file serves both clients, and a profile written for one
// makes the other answer InvalidParameterValue 4120 on every authenticated call
// while the public ones keep working. Reading both is what stops this package
// from repeating an hour that has already been spent.
func outscaleCredentials(profile string) (credentials, error) {
	path := filepath.Join(home(), ".osc", "config.json")
	raw, err := os.ReadFile(path) //nolint:gosec // the operator's own client config
	if err != nil {
		return credentials{}, fmt.Errorf("outscale: %w", err)
	}
	var file map[string]struct {
		AccessKey  string `json:"access_key"`
		SecretKey  string `json:"secret_key"`
		RegionName string `json:"region_name"`
		Region     string `json:"region"`
		Host       string `json:"host"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return credentials{}, fmt.Errorf("outscale: %s: %w", path, err)
	}
	if profile == "" {
		profile = "default"
	}
	p, known := file[profile]
	if !known {
		return credentials{}, fmt.Errorf("outscale: no profile %q in %s", profile, path)
	}
	region := p.RegionName
	if region == "" {
		region = p.Region
	}
	if region == "" {
		return credentials{}, fmt.Errorf("outscale: profile %q names no region", profile)
	}
	host := p.Host
	if host == "" {
		host = "outscale.com"
	}
	return credentials{
		key: p.AccessKey, secret: p.SecretKey, region: region,
		endpoint: fmt.Sprintf("https://api.%s.%s", region, host),
	}, nil
}

// exoscaleCredentials reads ~/.config/exoscale/exoscale.toml.
//
// Matched by line rather than parsed as TOML, for the same reason as Scaleway:
// the shape is flat and a dependency has to be earned. The zone decides the
// endpoint, because Exoscale's API is per zone — and because the zone list the
// server returns carries an api-endpoint the client follows, which is a
// separate trap documented in sign.go.
func exoscaleCredentials(profile string) (credentials, error) {
	path := filepath.Join(configHome(), "exoscale", "exoscale.toml")
	raw, err := os.ReadFile(path) //nolint:gosec // the operator's own client config
	if err != nil {
		return credentials{}, fmt.Errorf("exoscale: %w", err)
	}
	text := string(raw)
	if profile != "" {
		var found bool
		text, found = exoscaleAccount(text, profile)
		if !found {
			return credentials{}, fmt.Errorf("exoscale: no account %q in %s", profile, path)
		}
	}
	zone := os.Getenv("EXOSCALE_ZONE")
	if zone == "" {
		zone = "ch-gva-2"
	}
	c := credentials{
		key:      tomlValue(text, "key"),
		secret:   tomlValue(text, "secret"),
		region:   zone,
		endpoint: "https://api-" + zone + ".exoscale.com",
	}
	if c.secret == "" {
		return credentials{}, fmt.Errorf("exoscale: no secret in %s", path)
	}
	return c, nil
}

func exoscaleAccount(text, name string) (string, bool) {
	// Same quoting caution as tomlValue: the CLI writes single quotes.
	idx := -1
	for _, form := range []string{`name = "` + name + `"`, `name = '` + name + `'`, "name = " + name} {
		if i := strings.Index(text, form); i >= 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", false
	}
	rest := text[idx:]
	if next := strings.Index(rest[1:], "[[accounts]]"); next >= 0 {
		rest = rest[:next+1]
	}
	return rest, true
}

// tomlValue reads one key, whichever quoting the file uses.
//
// The exoscale CLI writes single quotes; a hand-edited file may carry double
// quotes or none. Accepting only one form made this package report "no secret"
// against a file that has one, which is the most misleading error it could
// produce — it accuses the operator's configuration of being incomplete when it
// is this reader that is narrow.
//
// TestTomlValueReadsEveryQuotingTheFileMayUse fails without the three cases.
func tomlValue(text, key string) string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(?:"([^"]*)"|'([^']*)'|(\S+))\s*$`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	for _, group := range m[1:] {
		if group != "" {
			return group
		}
	}
	return ""
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func configHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(home(), ".config")
}

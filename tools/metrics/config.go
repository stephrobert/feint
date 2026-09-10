package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// Config names one project's release assets, so the same collector serves
// feint, pepin and pavois without a line of code changing.
//
// JSON rather than YAML, for two reasons that both come from this repository:
// the zero-dependency rule (rule 6) leaves no YAML parser in reach, and every
// other configured artefact here is already JSON (coverage/, contracts/,
// tools/falsify/specs/, docs/limits-acks.json). A hand-rolled YAML subset would
// be a dependency written in-house, which is the worst of the two.
type Config struct {
	// Project is the short name the reports carry.
	Project string `json:"project"`
	// Repository is owner/name on GitHub.
	Repository string `json:"repository"`

	// BinaryPatterns match the assets that ARE the software. Each may carry
	// the named groups `os` and `arch`, which is how a report splits platforms
	// without the collector knowing any project's naming scheme:
	//
	//	^feint-(?P<os>linux|darwin)-(?P<arch>amd64|arm64)$
	BinaryPatterns []string `json:"binary_patterns"`

	// IgnorePatterns match the assets that are deliberately NOT counted:
	// checksums, signatures, SBOM, provenance. They are listed rather than
	// inferred, and that is the whole point of the design.
	IgnorePatterns []string `json:"ignore_patterns"`
}

// Kind is what an asset was classified as.
type Kind int

const (
	// KindUnknown is an asset that matched neither list. It is an error, never
	// a silent zero. See Classify.
	KindUnknown Kind = iota
	// KindBinary is the software itself, and the only thing that is counted.
	KindBinary
	// KindIgnored is a declared companion file.
	KindIgnored
)

// compiled holds a Config with its patterns compiled once.
type compiled struct {
	cfg    Config
	binary []*regexp.Regexp
	ignore []*regexp.Regexp
}

// LoadConfig reads and validates one project's description.
func LoadConfig(path string) (*compiled, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg.compile()
}

func (c Config) compile() (*compiled, error) {
	switch {
	case c.Project == "":
		return nil, fmt.Errorf("the configuration names no project")
	case c.Repository == "":
		return nil, fmt.Errorf("the configuration names no repository")
	case len(c.BinaryPatterns) == 0:
		// A collector with no binary pattern would classify every asset as
		// unknown and report nothing, which is a silent zero wearing a
		// configuration's clothes.
		return nil, fmt.Errorf("the configuration lists no binary_patterns, so nothing would ever be counted")
	}
	out := &compiled{cfg: c}
	for _, p := range c.BinaryPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("binary_patterns: %q: %w", p, err)
		}
		out.binary = append(out.binary, re)
	}
	for _, p := range c.IgnorePatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("ignore_patterns: %q: %w", p, err)
		}
		out.ignore = append(out.ignore, re)
	}
	return out, nil
}

// Classify answers what an asset is, and refuses to guess.
//
// The refusal is the mechanism, not a courtesy. A filter that only selects what
// it recognises reports a smaller number the day the release workflow starts
// publishing `feint-windows-amd64` or renames an asset, and a smaller number
// reads exactly like a drop in adoption. Here an asset that matched neither
// list stops the collection and says its name, so the packaging change is
// triaged by a human the way `Declined()` makes an unserved operation a
// decision rather than a silence.
//
// TestAnUnexpectedAssetStopsTheCollection fails without this.
func (c *compiled) Classify(name string) Kind {
	for _, re := range c.binary {
		if re.MatchString(name) {
			return KindBinary
		}
	}
	for _, re := range c.ignore {
		if re.MatchString(name) {
			return KindIgnored
		}
	}
	return KindUnknown
}

// Platform reads the `os` and `arch` named groups out of whichever binary
// pattern matched. Both are empty when the pattern declares no groups, which is
// allowed: a project that does not encode a platform in its asset names still
// gets totals and per-version figures.
func (c *compiled) Platform(name string) (goos, arch string) {
	for _, re := range c.binary {
		m := re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		for i, group := range re.SubexpNames() {
			switch group {
			case "os":
				goos = m[i]
			case "arch":
				arch = m[i]
			}
		}
		return goos, arch
	}
	return "", ""
}

package drift_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/drift"
)

func TestScanScalewaySDK(t *testing.T) {
	ops, err := drift.ScanScalewaySDK(filepath.Join("testdata", "fake-scaleway-sdk"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	names := make([]string, 0, len(ops))
	for _, op := range ops {
		names = append(names, op.Name)
	}

	want := []string{
		"instance/v1/API.CreateServer",
		"instance/v1/API.GetServer",
		"instance/v1/API.ListServers",
		"instance/v1/API.ServerAction",
		"instance/v1/API.UpdateServer",
		"instance/v1/ZonedAPI.ListVolumes",
		"rdb/v1/API.ListInstances",
		// The gateway, which lives in scw/ rather than under a product and
		// carries no version — the precedent scan_outscale.go sets.
		"scw/Client.GetAPIMetadata",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("unexpected surface\n got: %v\nwant: %v", names, want)
	}

	// The traps must not appear: a comment, a string literal, an unexported
	// method and a non-API receiver are all invisible to the real API surface.
	// So are the two shapes of client-side convenience — a poller, and a method
	// composing exported calls — and a constant accessor that reaches nothing.
	for _, ghost := range []string{"GhostFromAComment", "GhostFromAString", "internalHelper", "NotAnOperation", "updateServer", "WaitForServer", "ServerActionAndWait", "Zones",
		// The gateway's own two traps: the transport every call goes through,
		// and a formatter. Reusing the product walk's matcher reported both.
		"Client.Do", "Config.String"} {
		for _, name := range names {
			if strings.Contains(name, ghost) {
				t.Fatalf("scanner picked up %q, which is not an API operation", ghost)
			}
		}
	}
}

func TestScanScalewaySDKMissingCheckout(t *testing.T) {
	if _, err := drift.ScanScalewaySDK(filepath.Join("testdata", "does-not-exist")); err == nil {
		t.Fatal("expected a clear error when the SDK checkout is missing")
	}
}

func TestScanProductAndVersionAreCarried(t *testing.T) {
	ops, err := drift.ScanScalewaySDK(filepath.Join("testdata", "fake-scaleway-sdk"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, op := range ops {
		if op.Product == "" {
			t.Fatalf("operation %q lost its product: %+v", op.Name, op)
		}
		// The gateway is the exception, and it is asserted rather than skipped:
		// it MUST carry no version, because it declares none and inventing one
		// would be a fact nobody could check — the precedent scan_outscale.go
		// sets for the same reason. Everything else must carry one.
		if op.Product == "scw" {
			if op.Version != "" {
				t.Fatalf("the gateway operation %q carries version %q; it declares none",
					op.Name, op.Version)
			}
			continue
		}
		if op.Version == "" {
			t.Fatalf("operation %q lost its version: %+v", op.Name, op)
		}
	}
}

// The gateway walk counts what BUILDS a request, and nothing else.
//
// Both halves, because the first version of this walk got both wrong: it reused
// issuesRequest, which is written for the product packages, and that matcher
// found `Client.Do` — the transport every call goes through — and
// `Config.String` — a formatter — while missing `Client.GetAPIMetadata`, the one
// operation the walk exists for.
//
// The fake SDK's scw/client.go carries those three shapes on purpose.
func TestTheGatewayScanCountsWhatBuildsARequest(t *testing.T) {
	ops, err := drift.ScanScalewaySDK(filepath.Join("testdata", "fake-scaleway-sdk"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	gateway := make([]string, 0, 1)
	for _, op := range ops {
		if strings.HasPrefix(op.Name, "scw/") {
			gateway = append(gateway, op.Name)
			if op.Product != "scw" {
				t.Errorf("%s carries product %q, want scw", op.Name, op.Product)
			}
			// The gateway declares no API version, and inventing one would be a
			// fact nobody could check — the precedent scan_outscale.go states.
			if op.Version != "" {
				t.Errorf("%s carries version %q; the gateway declares none", op.Name, op.Version)
			}
		}
	}

	if !slices.Equal(gateway, []string{"scw/Client.GetAPIMetadata"}) {
		t.Fatalf("the gateway surface is %v, want exactly [scw/Client.GetAPIMetadata]. "+
			"Client.Do carries a request rather than building one, and Config.String "+
			"never sees one", gateway)
	}
}

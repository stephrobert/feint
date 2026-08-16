package resource_test

import (
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

// The two properties New exists for, pinned: the clocks agree at birth — a
// resource whose Created and Updated differ reads as already modified — and
// Attrs is ready to fill, because every pack writes into it on the next line
// and a nil map panics there.
func TestNewAlignsTheClocksAndReadiesAttrs(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	res := resource.New("id-1", "server", resource.Tenant{Provider: "example"}, "stopped", now)

	if !res.Created.Equal(now) || !res.Updated.Equal(now) {
		t.Errorf("clocks: Created=%v Updated=%v, want both %v", res.Created, res.Updated, now)
	}
	if res.Attrs == nil {
		t.Fatal("Attrs is nil: the first pack write into it panics")
	}
	res.Attrs["key"] = "value"
	if res.State != "stopped" || res.Kind != "server" || res.ID != "id-1" {
		t.Errorf("fields did not carry: %+v", res)
	}
}

package store

import (
	"bytes"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
)

func res(id, kind, zone string, created time.Time) *resource.Resource {
	return &resource.Resource{
		ID:      id,
		Kind:    kind,
		Tenant:  resource.Tenant{Provider: "scaleway", Project: "proj", Zone: zone},
		State:   "stopped",
		Created: created,
		Updated: created,
		Attrs:   map[string]any{"name": id},
	}
}

func TestPutGetDelete(t *testing.T) {
	s := New()
	now := time.Unix(1700000000, 0).UTC()
	s.Put(res("a", "server", "fr-par-1", now))

	got, ok := s.Get("scaleway", "server", "a")
	if !ok {
		t.Fatal("expected the resource to exist")
	}
	if got.Attrs["name"] != "a" {
		t.Fatalf("unexpected attrs: %v", got.Attrs)
	}
	if _, ok := s.Get("scaleway", "server", "missing"); ok {
		t.Fatal("expected a miss for an unknown ID")
	}
	if !s.Delete("scaleway", "server", "a") {
		t.Fatal("expected Delete to report the resource existed")
	}
	if s.Delete("scaleway", "server", "a") {
		t.Fatal("expected the second Delete to report a miss")
	}
}

func TestGetReturnsACopy(t *testing.T) {
	s := New()
	now := time.Unix(1700000000, 0).UTC()
	s.Put(res("a", "server", "fr-par-1", now))

	got, _ := s.Get("scaleway", "server", "a")
	got.Attrs["name"] = "mutated"

	again, _ := s.Get("scaleway", "server", "a")
	if again.Attrs["name"] != "a" {
		t.Fatalf("mutating a returned resource leaked into the store: %v", again.Attrs)
	}
}

func TestListIsFilteredAndOrdered(t *testing.T) {
	s := New()
	base := time.Unix(1700000000, 0).UTC()
	s.Put(res("c", "server", "fr-par-1", base.Add(2*time.Second)))
	s.Put(res("a", "server", "fr-par-1", base))
	s.Put(res("b", "server", "nl-ams-1", base.Add(time.Second)))
	s.Put(res("v", "volume", "fr-par-1", base))

	all := s.List("server", resource.Tenant{Provider: "scaleway"})
	if len(all) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(all))
	}
	if all[0].ID != "a" || all[1].ID != "b" || all[2].ID != "c" {
		t.Fatalf("expected creation order a,b,c, got %s,%s,%s", all[0].ID, all[1].ID, all[2].ID)
	}

	zoned := s.List("server", resource.Tenant{Provider: "scaleway", Zone: "fr-par-1"})
	if len(zoned) != 2 {
		t.Fatalf("expected 2 servers in fr-par-1, got %d", len(zoned))
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := New()
	now := time.Unix(1700000000, 0).UTC()
	s.Put(res("a", "server", "fr-par-1", now))
	s.Put(res("b", "server", "fr-par-1", now.Add(time.Second)))

	var buf bytes.Buffer
	if err := s.Snapshot(&buf); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	restored := New()
	if err := restored.Restore(&buf); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Len() != 2 {
		t.Fatalf("expected 2 resources after restore, got %d", restored.Len())
	}
	got, ok := restored.Get("scaleway", "server", "b")
	if !ok || !got.Created.Equal(now.Add(time.Second)) {
		t.Fatalf("restored resource lost its timestamps: %+v", got)
	}
}

// The store is the one place every pack's lifecycle already passes through,
// which is what makes its events the machine-checkable source of the behaviour
// evidence axis: a lifecycle is observed here, never declared by a suite.
func TestEveryTouchIsObservedAndSnapshotsAreNot(t *testing.T) {
	s := New()
	var seen []Event
	s.Observe(func(ev Event) { seen = append(seen, ev) })

	now := time.Now()
	r := res("thing-1", "thing", "z", now)
	s.Put(r)                              // created
	s.Put(r)                              // updated: it existed
	s.Get("scaleway", "thing", "thing-1") // read
	s.Get("scaleway", "thing", "absent")  // nothing: not found
	_ = s.Update("scaleway", "thing", "thing-1", func(res *resource.Resource) error {
		res.State = "running"
		return nil
	}) // updated
	s.List("thing", resource.Tenant{Provider: "scaleway"}) // listed
	s.Delete("scaleway", "thing", "thing-1")               // deleted
	s.Delete("scaleway", "thing", "thing-1")               // nothing: already gone

	var buf bytes.Buffer
	if err := s.Snapshot(&buf); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(&buf); err != nil {
		t.Fatal(err)
	}

	want := []string{EventCreated, EventUpdated, EventRead, EventUpdated, EventListed, EventDeleted}
	if len(seen) != len(want) {
		t.Fatalf("observed %d events, want %d: %+v", len(seen), len(want), seen)
	}
	for i, ev := range seen {
		if ev.Action != want[i] {
			t.Errorf("event %d is %q, want %q", i, ev.Action, want[i])
		}
		if ev.Action != EventListed && ev.ID != "thing-1" {
			t.Errorf("event %d does not name the resource: %+v", i, ev)
		}
		if ev.Provider != "scaleway" || ev.Kind != "thing" {
			t.Errorf("event %d does not carry the neutral coordinates: %+v", i, ev)
		}
	}
}

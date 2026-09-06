package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// AccountAliases (#700). FiltersImage and FiltersSnapshot declare it, the
// contract's own ReadImages example uses it (`AccountAliases: [Outscale]` with
// `ImageNames: [Ubuntu*, RockyLinux*]`), and both reads refused it here with
// the honest 4001 that names what they serve. Measured through osc-sdk-python
// on v0.12.1, and reproduced by the first test below before the fix.
//
// What the alias is, in this emulator. The catalogue's images already carried
// AccountAlias (#95: one of the nine fields the real cloud puts on every image,
// shapes/outscale.json), and a registered image or a snapshot carried none
// while sharing the catalogue's AccountId — the same owner, published two
// ways. One owner, one alias: every image and snapshot the emulator answers
// carries the catalogue's, and the filter that selects by owner selects
// consistently. The catalogue belongs to the emulated account by decision
// (catalog.go, PermissionsToLaunch names it as the owner), so
// `AccountAliases: [Outscale]` selects nothing here; docs/limits.md says so.
//
// Neither test names the alias: it is read off the catalogue, so the tests
// hold consistency rather than a value.

// catalogueAlias is the alias the catalogue's first image publishes, which is
// what every other object of the same owner is held to.
func catalogueAlias(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, out := post(t, ts, "ReadImages", `{}`)
	images, _ := out["Images"].([]any)
	if len(images) == 0 {
		t.Fatal("the image catalogue is empty, so nothing here has an owner to read")
	}
	first, _ := images[0].(map[string]any)
	alias, _ := first["AccountAlias"].(string)
	if alias == "" {
		t.Fatalf("the catalogue's first image carries no AccountAlias: %v", first)
	}
	return alias
}

// ownedInventory is inventory plus an image the client registered, so both
// reads answer a catalogue half and a client half.
func ownedInventory(t *testing.T, ts *httptest.Server) {
	t.Helper()
	inventory(t, ts)
	_, vms := post(t, ts, "ReadVms", `{}`)
	list, _ := vms["Vms"].([]any)
	if len(list) == 0 {
		t.Fatal("inventory created no Vm to register an image from")
	}
	vm, _ := list[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)
	if status, out := post(t, ts, "CreateImage", `{"ImageName":"own-image","VmId":"`+vmID+`"}`); status != http.StatusOK {
		t.Fatalf("CreateImage answered %d: %v", status, out)
	}
}

// count is how many objects a read answers under a key, after the response
// has been held to the contract.
func count(t *testing.T, ts *httptest.Server, action, body, key string) int {
	t.Helper()
	out := call(t, ts, contractDoc(t), action, body)
	list, _ := out[key].([]any)
	return len(list)
}

func TestReadImagesAndReadSnapshotsFilterByAccountAlias(t *testing.T) {
	ts := newServer(t)
	ownedInventory(t, ts)
	alias := catalogueAlias(t, ts)

	for _, read := range []struct{ action, key string }{
		{"ReadImages", "Images"}, {"ReadSnapshots", "Snapshots"},
	} {
		all := count(t, ts, read.action, `{}`, read.key)
		if all < 2 {
			t.Fatalf("%s answers %d objects unfiltered; the sweep below would prove nothing", read.action, all)
		}
		// The owner's alias selects everything the owner holds, catalogue and
		// client halves alike.
		if got := count(t, ts, read.action, `{"Filters":{"AccountAliases":["`+alias+`"]}}`, read.key); got != all {
			t.Errorf("%s AccountAliases [%s] answers %d of the %d objects that owner holds", read.action, alias, got, all)
		}
		// And an alias nobody here carries selects nothing: the contract's own
		// example asks for Outscale's images, and this catalogue is not theirs.
		if got := count(t, ts, read.action, `{"Filters":{"AccountAliases":["Outscale"]}}`, read.key); got != 0 {
			t.Errorf("%s AccountAliases [Outscale] answers %d objects, and nothing here is Outscale's", read.action, got)
		}
	}
}

// Every image and every snapshot carries the one owner's alias next to its
// AccountId, whether the catalogue or a client made it. Before #700 the
// catalogue's images carried it and nothing else did.
func TestEveryImageAndSnapshotCarriesItsOwnerAlias(t *testing.T) {
	ts := newServer(t)
	ownedInventory(t, ts)
	alias := catalogueAlias(t, ts)

	for _, read := range []struct{ action, key, id string }{
		{"ReadImages", "Images", "ImageId"}, {"ReadSnapshots", "Snapshots", "SnapshotId"},
	} {
		_, out := post(t, ts, "ReadImages", `{}`)
		if read.action == "ReadSnapshots" {
			_, out = post(t, ts, "ReadSnapshots", `{}`)
		}
		list, _ := out[read.key].([]any)
		if len(list) < 2 {
			t.Fatalf("%s answers %d objects; a catalogue and a client half were expected", read.action, len(list))
		}
		accounts := map[string]bool{}
		for _, entry := range list {
			object, _ := entry.(map[string]any)
			accounts[object["AccountId"].(string)] = true
			if got, _ := object["AccountAlias"].(string); got != alias {
				t.Errorf("%s: %s carries AccountAlias %q, and its owner's alias is %q", read.action, object[read.id], got, alias)
			}
		}
		if len(accounts) != 1 {
			t.Errorf("%s answers objects of %d accounts; this emulator has one", read.action, len(accounts))
		}
	}
}

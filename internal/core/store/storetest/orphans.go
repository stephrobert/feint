package storetest

import (
	"fmt"
	"sort"

	"github.com/stephrobert/feint/internal/core/resource"
)

// An exclusive resource has one live owner, and this is the third time that
// sentence has been written into a pack rather than into the core.
//
// Scaleway detaches a deleted server's volumes and, since #214, releases its
// private NICs. Outscale detaches a terminated Vm's interfaces and, since #215,
// unlinks its volumes. Each of those was found separately, by a different
// symptom, months apart — and every one of them is the same fact: the pack
// re-checked exclusivity on every link and on no death.
//
// What the symptom looks like from a client is worth stating, because it is the
// reason this matters more than tidiness. The dependent record names an owner
// that answers 404, so it is invisible to every listing scoped by that owner —
// and it goes on holding something exclusive: an address the allocator will not
// reissue, a volume LinkVolume refuses to attach anywhere else, a private network
// that refuses to be deleted while something references it. There is no call left
// that frees it. The user restarts the emulator.
//
// Sweep answers what is incoherent about a set of resources on its own terms.
// This needs one thing Sweep cannot know: which attribute or runtime key names an
// owner, and of what kind. So the pack declares that, and the invariant lives
// here — the same split as Gone and Shared, for the same reason. A fourth pack
// that forgets a death path fails a control it never had to remember to write.

// Ownership is what a pack knows and the core cannot: that this resource belongs
// to another, and which one.
//
// Returning ok=false means "this resource claims no owner", which is the normal
// answer for most of them. Kind is compared as the pack stores it.
type Ownership func(res *resource.Resource) (kind, id string, ok bool)

// Orphans reports every resource whose declared owner is not in the store.
//
// Gone is honoured for the owner rather than for the dependent: a pack that keeps
// a record its API calls dead — Outscale keeps a terminated Vm readable, because
// the Terraform provider polls for that state — still owns nothing. A volume
// still linked to a terminated Vm is exactly the defect this looks for, so an
// owner that Gone reports as dead counts as absent.
func Orphans(resources []*resource.Resource, owner Ownership, gone Gone) []string {
	if owner == nil {
		return nil
	}

	alive := map[string]bool{}
	for _, res := range resources {
		if res == nil {
			continue
		}
		if gone != nil && gone(res) {
			continue
		}
		alive[res.Kind+"/"+res.ID] = true
	}

	var found []string
	for _, res := range resources {
		if res == nil {
			continue
		}
		kind, id, ok := owner(res)
		if !ok || id == "" {
			continue
		}
		if alive[kind+"/"+id] {
			continue
		}
		found = append(found, fmt.Sprintf(
			"%s %s still names %s %s, which no longer exists: whatever it holds exclusively "+
				"is held for ever, and no client call can release it",
			res.Kind, res.ID, kind, id))
	}
	sort.Strings(found)
	return found
}

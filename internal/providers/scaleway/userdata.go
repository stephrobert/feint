package scaleway

import (
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// User data is how a client ships a boot script to a server, and the Terraform
// provider reads the whole set back right after creating one: without these
// routes the apply fails on a bare 404 with no indication of what was missing.
//
// The shapes are not the usual JSON envelopes. A single key is transferred as a
// raw body, both ways, because the value is an arbitrary blob (a #cloud-config,
// a shell script); only the key listing is JSON. Wrapping the value in JSON is
// the mistake that makes the SDK hand a client a quoted string it never wrote.
//
// The key that matters is "cloud-init": Scaleway feeds it to cloud-init at boot,
// and so does the machine driver, which is what makes a userdata script actually
// run here rather than merely round-trip.

// userDataPrefix namespaces user data inside Runtime, which views never
// serialize: the server shape has no user_data field, and leaking one would give
// a client a field the SDK cannot decode.
const userDataPrefix = "userdata:"

// CloudInitKey is the key Scaleway reads at boot.
const CloudInitKey = "cloud-init"

// maxUserDataSize caps a single value. Scaleway's own limit is larger; the point
// here is that an emulator must not be turned into a memory sink by a client
// that streams into it.
const maxUserDataSize = 512 << 10

func (p *Pack) listServerUserData(w http.ResponseWriter, r *http.Request) {
	res, ok := p.serverOf(w, r)
	if !ok {
		return
	}

	keys := make([]string, 0, len(res.Runtime))
	for k := range res.Runtime {
		if name, found := strings.CutPrefix(k, userDataPrefix); found {
			keys = append(keys, name)
		}
	}
	// Sorted: the API serves a set, and an order that changed between two reads
	// would show up as a diff in a client that stores the list.
	sort.Strings(keys)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"user_data": keys})
}

func (p *Pack) getServerUserData(w http.ResponseWriter, r *http.Request) {
	res, ok := p.serverOf(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	value, found := res.Runtime[userDataPrefix+key]
	if !found {
		writeNotFound(w, "user_data", key)
		return
	}

	// Raw, not JSON: the value is whatever the client stored.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, value)
}

func (p *Pack) setServerUserData(w http.ResponseWriter, r *http.Request) {
	res, ok := p.serverOf(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "key", Reason: "required"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxUserDataSize+1))
	if err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if len(body) > maxUserDataSize {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "body",
			Reason:       "constraint",
			HelpMessage:  "user data is larger than the emulator accepts",
		})
		return
	}

	// Inside the store lock, never Get-mutate-Put: Put re-inserted a server
	// deleted while the body was uploading, and the wholesale write erased a
	// concurrent write to any other field of the same server after its 200 —
	// the scalar half of the lost-update family (#295).
	err = p.env.Store.Update(Name, kindServer, res.ID, func(stored *resource.Resource) error {
		if stored.Runtime == nil {
			stored.Runtime = map[string]string{}
		}
		stored.Runtime[userDataPrefix+key] = string(body)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "server", res.ID)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) deleteServerUserData(w http.ResponseWriter, r *http.Request) {
	res, ok := p.serverOf(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	// The existence check and the removal are one critical section, same
	// reason as setServerUserData (#295).
	err := p.env.Store.Update(Name, kindServer, res.ID, func(stored *resource.Resource) error {
		if _, found := stored.Runtime[userDataPrefix+key]; !found {
			return errUserDataMissing
		}
		delete(stored.Runtime, userDataPrefix+key)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "user_data", key)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// errUserDataMissing is the not-found the delete answers from inside the store
// lock, where the key it judges cannot move (#295).
var errUserDataMissing = errors.New("no user data under that key")

// userDataOf returns the value a client stored under key, empty when there is
// none. The machine driver reads "cloud-init" through it.
func userDataOf(res *resource.Resource, key string) string {
	return res.Runtime[userDataPrefix+key]
}

// serverOf resolves the {id} segment of a server sub-resource, writing the error
// itself so handlers stay a single early return.
func (p *Pack) serverOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindServer, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return nil, false
	}
	return res, true
}

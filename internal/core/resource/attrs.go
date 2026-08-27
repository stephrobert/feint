package resource

import "encoding/json"

// Reading a stored value back, once, for every pack there will ever be (#542).
//
// Attrs is a map[string]any and the store's snapshot is JSON, so a resource
// that has crossed store.Snapshot/store.Restore — the door `feint snapshot
// load` and `PUT /_feint/state` both go through, and the format snapshot.go
// documents as meant to outlive its instance — comes back decoded by
// encoding/json. Measured on 2026-08-27, on a resource carrying one attribute
// of each type a pack writes:
//
//	anInt   int     -> float64      aBoolTrue  bool   -> bool
//	anInt64 int64   -> float64      aString    string -> string
//	aUint32 uint32  -> float64      aMap[n]    int    -> float64
//
// So a `.(int)` assertion on a restored number yields ok=false and 0, while
// `.(string)` and `.(bool)` are unaffected — which is why the readers below
// cover numbers and nothing else, and why internal/cli's
// TestNoPackReadsAStoredNumberByAssertion refuses a numeric assertion in a
// pack and leaves the other two alone. A reader that repaired nothing would be
// churn at every call site it reached.
//
// It lives here rather than in a pack because seven packs' worth of code had
// already discovered it separately — exoscale's intOf and int64Of, exoscale's
// and outscale's numOf, scaleway's positionOf and portValue, and the fake
// fourth pack's portOf, each with its own name and its own sentence — and
// outscale's own volumes.go still read a size with a plain assertion three
// files away from a tolerant helper of the same pack. That is CLAUDE.md's
// factorisation rule measured: written seven times, the eighth author writes
// it wrong, and nothing carries the lesson to a pack that does not exist yet.
//
// No provider name enters any of this: a number that crossed JSON is a shape,
// not a vocabulary (rule 5).

// Number reads a stored number back as a float64, whatever Go type it was
// written as and whichever side of a snapshot it is being read on.
//
// The value form, for a number that is not a direct attribute of a resource:
// one inside a nested map a pack stored (a listener's port, a device mapping's
// size), or one a caller already holds. Int and Int64 are the resource form.
//
// json.Number is accepted because a decoder configured with UseNumber produces
// one — the proxy and the transcript readers are configured that way — and
// because outscale's numOf accepted it before this file existed. The store's
// own Restore does not produce one; it produces float64.
//
// Anything that is not a number reads 0, which is the same answer the comma-ok
// assertions this replaces gave, and the same answer an absent key gives.
func Number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int8:
		return float64(n)
	case int16:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint8:
		return float64(n)
	case uint16:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

// Int reads res.Attrs[key] back as an int.
//
// A nil resource, an absent key and a value that is not a number all read 0,
// so a caller that wants to tell "absent" from "zero" asks Attrs itself — the
// same distinction the comma-ok form offered, kept available rather than
// hidden behind a reader that answers one thing.
//
// Exact for every magnitude these APIs name: float64 carries integers to 2^53,
// and a volume size, a port or a VXLAN identifier is nowhere near it. A value
// beyond that has already lost its precision in the snapshot, before any
// reader sees it.
func Int(res *Resource, key string) int {
	return int(attr(res, key))
}

// Int64 is Int for the packs whose upstream field is a 64-bit integer — a
// block volume's size in bytes, a pool's disk size. Same limits as Int.
func Int64(res *Resource, key string) int64 {
	return int64(attr(res, key))
}

// Uint64 is Int for the packs whose upstream field is unsigned — Scaleway
// spells a volume size and a snapshot size uint64.
//
// A stored value below zero reads 0 rather than wrapping. It cannot arrive
// from any handler of this emulator, and that is exactly why it is guarded: a
// snapshot is designed to be loaded into another instance, so what comes back
// through Restore is untrusted input and a negative number converted to uint64
// is 18 million million million bytes of volume.
func Uint64(res *Resource, key string) uint64 {
	n := attr(res, key)
	if !(n >= 0) { // false for NaN too, which is why it is written this way
		return 0
	}
	return uint64(n)
}

func attr(res *Resource, key string) float64 {
	if res == nil {
		return 0
	}
	return Number(res.Attrs[key])
}

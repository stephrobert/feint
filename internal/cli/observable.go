package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The observable state of a lab, normalised so that two reads of the same
// state are equal.
//
// This exists before `feint checkpoint` and `feint reset` (#853) because
// without it those two commands cannot be honest. The issue says so in one
// line — "reset is not successful because JSON was restored" — and a reset
// that reported success from the absence of an error would be exactly the
// verdict this repository refuses: an operation's own opinion of itself.
//
// So the verdict is a COMPARISON, and this file is the thing compared.
//
// ## What is in it, and why those three
//
//   - the control plane, from /_feint/resources: what the API says exists;
//   - the bytes, read THROUGH the doors: what a client can fetch;
//   - the host, from Runtime.Survey: what the emulator actually created.
//
// Any two of the three without the third is a reset that can lie. Restoring
// the store alone leaves containers running that the API no longer mentions;
// restoring the store and the host leaves bytes from a later test in a bucket
// the checkpoint had empty.
//
// ## Why the digest is computed here and not read from the listing
//
// The first version of this file took the digest from ListObjectsV2's ETag,
// and `internal/s3/list.go` emits no ETag at all: the field was always empty,
// so two different contents under one key compared EQUAL and a reset could
// report convergence over bytes that had changed. The commit message claimed
// objects were compared by digest. They were not.
//
// It survived review and a passing test because the test built observedObject
// values by hand, with digests in them. A comparison is only worth the
// observation under it, and an observation nobody made is a field with a zero
// value. Found by an external audit of the merged code, not here.
//
// So each object is FETCHED and hashed by the observer. Not the store's own
// recorded digest and not the checksum header the door offers: what is being
// judged is whether a client gets the same bytes back, and a digest supplied
// by the subject cannot answer that. The size travels with it as a second,
// independent signal and is part of the comparison key, which the first
// version also dropped.
//
// It costs one GET per object. That is acceptable for a verdict on a local
// store with a byte budget, and the alternative measured nothing.
//
// TestTwoObjectsOfOneSizeAndDifferentBytesDoNotConverge fails without this.
//
// ## Why the bytes are read through the door rather than from the store
//
// The authoritative view of an object is the one a client gets, and this
// project's whole claim is that a client cannot tell the difference. A
// comparison that read the store directly would pass while the door served
// something else, which is the half that matters to a test using it.
//
// It does mean the comparison depends on the door answering. That is not a
// circularity worth avoiding: bytes a door cannot serve are bytes no client
// can observe, and a reset whose doors are broken has not converged.
//
// ## What is deliberately NOT in it
//
// Created and Updated timestamps, because a restored resource carries the
// instant it was restored and a comparison including them could never be
// equal. What survives of time is ordering, and the store already keeps that
// in the identifiers. Attrs are not compared field by field either: a pack's
// own view can hold a derived value, and a reset is about the state, not about
// how a pack renders it. The identity, the kind and the state are what a test
// builds on.
//
// TestTwoReadsOfOneLabAreEqual and TestADifferenceIsNamedWhereverItIs fail
// without this.

// observed is one lab's normalised state.
type observed struct {
	Resources []observedResource `json:"resources"`
	Objects   []observedObject   `json:"objects"`
	Machines  []string           `json:"machines"`
	Networks  []string           `json:"networks"`
	Firewalls []string           `json:"firewalls"`
}

// observedResource is a control-plane record, without what cannot be equal.
type observedResource struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	State string `json:"state,omitempty"`
	// Backing is the host object this record names, and it is what ties the
	// two halves together: a resource whose container changed under it is a
	// resource a reset did not reconcile, even when both lists are the same
	// length.
	Backing string `json:"backing,omitempty"`
}

// observedObject is one object, identified by its content rather than by a
// length: two objects of the same size are not the same object, and a reset
// that swapped them would otherwise compare equal.
type observedObject struct {
	Door   string `json:"door"`
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	// Digest is a SHA-256 this observer computed over the bytes a GET
	// delivered. Never the store's own record of it, and never the door's
	// checksum header: a digest supplied by the subject cannot answer whether
	// the subject serves the bytes it claims.
	Digest string `json:"digest"`
	// Size is what the GET actually delivered, and Listed is what the
	// inventory said. They are compared separately so an inventory that
	// disagrees with the bytes is a difference rather than a silent one.
	Size   int64 `json:"size"`
	Listed int64 `json:"listed"`
}

// observeLab reads the three halves and normalises them.
//
// A runtime that answers nothing is not an error: `--vm off` is the ordinary
// case and its host half is legitimately empty. An unreachable emulator IS an
// error, because then nothing was observed and a comparison would be between
// two silences.
func observeLab(ctx context.Context, endpoint string, rt machine.Runtime) (observed, error) {
	var state observed

	inventory, err := readInventoryView(endpoint)
	if err != nil {
		return observed{}, fmt.Errorf("reading the control plane: %w", err)
	}
	for _, view := range inventory {
		state.Resources = append(state.Resources, observedResource{
			Kind: view.Kind, ID: view.ID, State: view.State,
			Backing: backingOf(view.Runtime),
		})
	}

	objects, err := observeObjects(ctx, endpoint)
	if err != nil {
		return observed{}, fmt.Errorf("reading the bytes: %w", err)
	}
	state.Objects = objects

	// Survey names only what the emulator labelled, and its own test asserts
	// it issues no mutating command. A driver that cannot survey answers
	// asked=false, and the host half stays empty rather than guessed.
	if left, asked, err := rt.Survey(ctx); err != nil {
		return observed{}, fmt.Errorf("surveying the host: %w", err)
	} else if asked {
		state.Machines = append(state.Machines, left.Machines...)
		state.Networks = append(state.Networks, left.Networks...)
		state.Firewalls = append(state.Firewalls, left.Firewalls...)
	}

	state.normalise()
	return state, nil
}

// normalise sorts every list so that two reads of one state are equal.
func (o *observed) normalise() {
	slices.SortFunc(o.Resources, func(a, b observedResource) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(o.Objects, func(a, b observedObject) int {
		for _, pair := range [][2]string{{a.Door, b.Door}, {a.Bucket, b.Bucket}, {a.Key, b.Key}} {
			if c := strings.Compare(pair[0], pair[1]); c != 0 {
				return c
			}
		}
		return 0
	})
	slices.Sort(o.Machines)
	slices.Sort(o.Networks)
	slices.Sort(o.Firewalls)
}

// backingOf answers the host object a record names, whatever key the pack
// used for it.
//
// Sorted and joined rather than read from one known key: the Runtime map is a
// pack's own vocabulary, three packs spell it differently, and a hardcoded key
// here would silently observe nothing for the fourth pack. What matters for a
// comparison is that the value changes when the host object changes.
func backingOf(runtime map[string]string) string {
	if len(runtime) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(runtime))
	for key, value := range runtime {
		pairs = append(pairs, key+"="+value)
	}
	slices.Sort(pairs)
	return strings.Join(pairs, " ")
}

// readInventoryView fetches /_feint/resources with the fields a comparison
// needs. up.go's readInventory drops the identity and the state, which are the
// two a reset is judged on, so this reads the fuller view rather than widening
// a struct three ready conditions already depend on.
func readInventoryView(endpoint string) ([]emulatorResourceView, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/_feint/resources")
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the inventory answered %d", res.StatusCode)
	}
	var body struct {
		Resources []emulatorResourceView `json:"resources"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxStateBody)).Decode(&body); err != nil {
		return nil, err
	}
	return body.Resources, nil
}

// emulatorResourceView is the part of emulator.ResourceView a comparison uses.
// Declared here rather than imported so that a field added upstream cannot
// silently join the comparison: a reset's verdict must change only when
// somebody decides it does.
type emulatorResourceView struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	State   string            `json:"state"`
	Runtime map[string]string `json:"runtime"`
}

// observeObjects lists every door's buckets and their objects, through the
// doors.
//
// An emulator serving no doors answers an empty list and no error: object
// storage is opt-in, and a lab without it has no bytes to converge.
func observeObjects(ctx context.Context, endpoint string) ([]observedObject, error) {
	report, err := fetchS3(endpoint)
	if err != nil {
		return nil, err
	}
	if !report.Enabled || len(report.Doors) == 0 {
		return nil, nil
	}
	client := &http.Client{Timeout: 30 * time.Second}
	var out []observedObject
	for _, door := range report.Doors {
		base := strings.TrimSuffix(endpoint, "/") + door.Path
		buckets, err := listBuckets(ctx, client, base)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", door.Path, err)
		}
		for _, bucket := range buckets {
			objects, err := listObjects(ctx, client, base, bucket)
			if err != nil {
				return nil, fmt.Errorf("%s%s: %w", door.Path, bucket, err)
			}
			for _, object := range objects {
				digest, size, err := fetchDigest(ctx, client, base+bucket+"/"+object.Key)
				if err != nil {
					return nil, fmt.Errorf("%s%s/%s: %w", door.Path, bucket, object.Key, err)
				}
				// The listing's size travels too, and a disagreement between
				// it and what the GET delivered is itself a difference: a
				// door whose inventory and bytes disagree has not converged
				// whatever the digests say.
				out = append(out, observedObject{
					Door: strings.Trim(door.Path, "/"), Bucket: bucket,
					Key: object.Key, Digest: digest, Size: size,
					Listed: object.Size,
				})
			}
		}
	}
	return out, nil
}

type bucketListing struct {
	Buckets struct {
		Bucket []struct {
			Name string `xml:"Name"`
		} `xml:"Bucket"`
	} `xml:"Buckets"`
}

type objectListing struct {
	Contents []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
	IsTruncated bool   `xml:"IsTruncated"`
	NextToken   string `xml:"NextContinuationToken"`
}

func listBuckets(ctx context.Context, client *http.Client, base string) ([]string, error) {
	var listing bucketListing
	if err := getXML(ctx, client, base, &listing); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(listing.Buckets.Bucket))
	for _, bucket := range listing.Buckets.Bucket {
		names = append(names, bucket.Name)
	}
	slices.Sort(names)
	return names, nil
}

func listObjects(ctx context.Context, client *http.Client, base, bucket string) ([]struct {
	Key  string `xml:"Key"`
	Size int64  `xml:"Size"`
}, error,
) {
	var all []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	}
	// Paginated, because a comparison that read the first page only would
	// report two different stores as equal — the exact shape of a verdict
	// measuring its own window rather than its subject.
	token := ""
	for page := 0; page < 1000; page++ {
		url := base + bucket + "?list-type=2"
		if token != "" {
			url += "&continuation-token=" + token
		}
		var listing objectListing
		if err := getXML(ctx, client, url, &listing); err != nil {
			return nil, err
		}
		all = append(all, listing.Contents...)
		if !listing.IsTruncated || listing.NextToken == "" {
			return all, nil
		}
		token = listing.NextToken
	}
	return nil, fmt.Errorf("%s did not stop paginating", bucket)
}

func getXML(ctx context.Context, client *http.Client, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %d", res.StatusCode)
	}
	return xml.NewDecoder(io.LimitReader(res.Body, maxStateBody)).Decode(into)
}

// diff names every difference, in both directions.
//
// Every one of them, and not the first: a reset reports exactly what it could
// not reconcile (#853), and a verdict that stopped at the first difference
// would send an operator round the loop once per problem. The wording says
// which side a thing is on, because "extra" and "missing" need opposite fixes.
func (o observed) diff(other observed) []string {
	var out []string

	resourceKey := func(r observedResource) string {
		return r.Kind + " " + r.ID + " state=" + r.State + " backing=[" + r.Backing + "]"
	}
	objectKey := func(ob observedObject) string {
		// Every field, and the size twice. The first version compared the
		// door, the bucket, the key and a digest that was always empty, so it
		// compared nothing about the content at all.
		return fmt.Sprintf("%s/%s/%s digest=%s bytes=%d listed=%d",
			ob.Door, ob.Bucket, ob.Key, ob.Digest, ob.Size, ob.Listed)
	}

	out = append(out, compareSets("resource", mapAll(o.Resources, resourceKey), mapAll(other.Resources, resourceKey))...)
	out = append(out, compareSets("object", mapAll(o.Objects, objectKey), mapAll(other.Objects, objectKey))...)
	out = append(out, compareSets("machine", o.Machines, other.Machines)...)
	out = append(out, compareSets("network", o.Networks, other.Networks)...)
	out = append(out, compareSets("firewall", o.Firewalls, other.Firewalls)...)
	return out
}

func mapAll[T any](items []T, key func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, key(item))
	}
	return out
}

// compareSets answers what is extra on the right and what is missing from it.
func compareSets(kind string, want, got []string) []string {
	var out []string
	for _, item := range got {
		if !slices.Contains(want, item) {
			out = append(out, kind+" present and not in the checkpoint: "+item)
		}
	}
	for _, item := range want {
		if !slices.Contains(got, item) {
			out = append(out, kind+" in the checkpoint and not present: "+item)
		}
	}
	return out
}

// fetchDigest reads an object through its door and answers what arrived.
//
// The hash is computed over the response body as it streams, so nothing the
// door says about the bytes is taken on trust, and the byte count comes from
// the hash's own writer rather than from a header.
func fetchDigest(ctx context.Context, client *http.Client, url string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("the door answered %d", res.StatusCode)
	}
	sum := sha256.New()
	size, err := io.Copy(sum, io.LimitReader(res.Body, maxObservedObjectBytes))
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(sum.Sum(nil)), size, nil
}

// maxObservedObjectBytes bounds one object a verdict will read.
//
// Generous rather than tight: the store's own per-object limit is 256 MiB by
// default, and a verdict that silently truncated at a smaller bound would
// compare the first N bytes of two objects and call them equal — the same
// class of defect as the empty digest this replaces.
const maxObservedObjectBytes = 512 << 20

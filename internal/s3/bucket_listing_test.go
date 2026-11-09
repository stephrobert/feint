package s3

import (
	"encoding/xml"
	"net/http"
	"testing"
)

// A door that declares no configuration shapes still enumerates its buckets,
// and the buckets it creates carry the date that enumeration renders.
//
// This is the test the decoupling needed and did not have. ListBuckets used to
// sit behind Config.BucketConfiguration, and so did the creation metadata, and
// `git log -S` names the commit that put them there: 7c3e087, "prove private
// Scaleway bucket lifecycle", which measured that provider's ACL, tagging and
// list shapes in one go. Opportunity, not meaning — the field's name says "the
// configuration of a bucket" and it was read as "this door may enumerate
// buckets".
//
// It cost a real client. octl v0.0.32 opens with ListBuckets and reaches the
// OOS door at exactly the canonical bare endpoint, captured on the wire:
//
//	GET /_feint/s3/outscale/eu-west-2/?x-id=ListBuckets
//
// and got 400 "invalid bucket or object key", because the Outscale profile
// leaves BucketConfiguration off on purpose: that switch also turns on
// discovery answers whose OOS shapes nobody has recorded. Then, once the
// listing was served, CreateBucket answered 200 and the ListBuckets after it
// answered 501 "legacy bucket listing has no measured creation metadata" —
// two gestures of one client disagreeing about one bucket.
//
// The fixture here carries BucketConfiguration at its zero value, which is why
// it is the right witness: it IS an OOS-shaped door.
//
// tools/falsify/specs/oos-door-serves-its-listing.json mutates all three
// properties, and each one reds this test. The first version of that spec
// aimed at TestMountedBucketListingAcceptsOnlyItsCanonicalBareEndpoint and all
// three mutations stayed green — a spec pointed at a test that does not cover
// its subject proves nothing while looking exactly like proof.
func TestADoorWithoutConfigurationStillListsAndDatesItsBuckets(t *testing.T) {
	s, _ := fixture(t)
	if s.cfg.BucketConfiguration {
		t.Fatal("this witness is only meaningful on a door that declares no configuration shapes")
	}

	type listed struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Buckets []struct {
			Name         string
			CreationDate string
		} `xml:"Buckets>Bucket"`
	}

	// The canonical bare endpoint, with the x-id the AWS SDK appends. Both
	// forms the client sends.
	for _, path := range []string{"/", "/?x-id=ListBuckets"} {
		w := request(t, s, http.MethodGet, path, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: the listing was refused with %d: %s", path, w.Code, w.Body.String())
		}
		var answer listed
		if err := xml.Unmarshal(w.Body.Bytes(), &answer); err != nil {
			t.Fatalf("%s: the listing is not a ListAllMyBucketsResult: %v %s", path, err, w.Body.String())
		}
		if len(answer.Buckets) != 0 {
			t.Fatalf("%s: an empty door listed %d bucket(s)", path, len(answer.Buckets))
		}
	}

	// A bucket created through this door, and the same listing finding it with
	// a date. Without the creation metadata the listing answers 501 rather than
	// omitting the bucket, which is the shape of the defect octl met.
	if w := request(t, s, http.MethodPut, "/listed-bucket", nil, nil); w.Code != http.StatusOK {
		t.Fatalf("CreateBucket was refused with %d: %s", w.Code, w.Body.String())
	}
	w := request(t, s, http.MethodGet, "/?x-id=ListBuckets", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("the listing after a create was refused with %d: %s", w.Code, w.Body.String())
	}
	var answer listed
	if err := xml.Unmarshal(w.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the listing after a create does not decode: %v %s", err, w.Body.String())
	}
	if len(answer.Buckets) != 1 || answer.Buckets[0].Name != "listed-bucket" {
		t.Fatalf("the created bucket is not the one bucket listed: %s", w.Body.String())
	}
	if answer.Buckets[0].CreationDate == "" {
		t.Fatalf("the created bucket carries no creation date: %s", w.Body.String())
	}

	// And the listing stays a listing. A query it never measured must not be
	// read as one, or every unrouted GET becomes a bucket enumeration — the
	// mirror of the gate this test exists to keep removed.
	for _, path := range []string{
		"/?x-id=ListBuckets&unmeasured=1",
		"/?acl=",
		"/?x-id=ListBuckets&x-id=ListBuckets",
	} {
		if w := request(t, s, http.MethodGet, path, nil, nil); w.Code == http.StatusOK &&
			xml.Unmarshal(w.Body.Bytes(), &answer) == nil && answer.XMLName.Local == "ListAllMyBucketsResult" {
			t.Fatalf("%s was served as a bucket listing: %s", path, w.Body.String())
		}
	}
}

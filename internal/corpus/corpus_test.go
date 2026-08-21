package corpus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/sshkey"
	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/transcript"
)

// doc is a small stand-in for a provider's API description: two paths, one
// enumerated value, one literal segment that must survive.
func doc(t *testing.T) *contract.Doc {
	t.Helper()
	const raw = `{
	  "provider": "test",
	  "pathPrefix": "",
	  "operations": {
	    "test.CreateThing": {"path": "/thing/v1/zones/{zone}/things", "method": "POST"},
	    "test.GetThing":    {"path": "/thing/v1/zones/{zone}/things/{thing_id}", "method": "GET"},
	    "test.ListThings":  {"path": "/thing/v1/zones/{zone}/things", "method": "GET",
	      "query": {"order_by": {"ref": "test.OrderBy"}}}
	  },
	  "schemas": {"test.OrderBy": {"closed": false, "type": "string",
	    "enum": ["created_at_desc", "created_at_asc"]}}
	}`
	parsed, err := contract.Read(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func options(t *testing.T) Options {
	t.Helper()
	return Options{Doc: doc(t), Vocabulary: []string{"fr-par-1"}}
}

// A recording of one create and one read of what it created.
func recording() []trace.Exchange {
	return []trace.Exchange{
		{
			Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
			Host: "api.example.com",
			Req: &trace.Message{
				Headers: map[string]string{"Content-Type": "application/json", "User-Agent": "scaleway-cli/2.56.3"},
				Body:    map[string]any{"name": "production-database", "project_id": "3f2a91c4-77b0-4d19-9c2e-51ab8e0d64f7"},
			},
			Res: &trace.Message{Body: map[string]any{
				"id":         "6d4c02be-9a15-4f83-8b7d-2e91c40fa5d8",
				"name":       "production-database",
				"address":    "51.15.0.1",
				"subnet":     "172.16.32.0/22",
				"created_at": "2026-08-21T09:12:33Z",
				"size":       json.Number("10000000000"),
				"protected":  true,
			}},
		},
		{
			Method: "GET", Path: "/thing/v1/zones/fr-par-1/things/6d4c02be-9a15-4f83-8b7d-2e91c40fa5d8",
			Query: "order_by=created_at_desc&page=1", Status: 200,
			Res: &trace.Message{Body: map[string]any{"id": "6d4c02be-9a15-4f83-8b7d-2e91c40fa5d8"}},
		},
	}
}

func sanitise(t *testing.T, exs []trace.Exchange) ([]trace.Exchange, Report) {
	t.Helper()
	out, rep, err := Sanitise(exs, options(t))
	if err != nil {
		t.Fatal(err)
	}
	return out, rep
}

// Every value of the account goes, and the shape of each one stays.
//
// The two halves are one test on purpose: a sanitiser that dropped the shape
// would pass the first half and produce a transcript no replay can reissue,
// which is the museum piece #351 names.
func TestEveryValueIsReplacedByOneOfTheSameShape(t *testing.T) {
	out, _ := sanitise(t, recording())
	body, _ := out[0].Res.Body.(map[string]any)

	for _, gone := range []string{"production-database", "6d4c02be-9a15-4f83-8b7d-2e91c40fa5d8",
		"51.15.0.1", "172.16.32.0/22", "3f2a91c4-77b0-4d19-9c2e-51ab8e0d64f7", "api.example.com"} {
		if raw, _ := json.Marshal(out); bytes.Contains(raw, []byte(gone)) {
			t.Errorf("the sanitised transcript still carries %q", gone)
		}
	}

	id, _ := body["id"].(string)
	if !strings.HasPrefix(id, syntheticUUIDPrefix) {
		t.Errorf("the identifier became %q, which is not a UUID: every read that follows would 404", id)
	}
	if addr, _ := body["address"].(string); !syntheticV4.Contains(netip.MustParseAddr(addr)) {
		t.Errorf("the address became %q, which is not an address", addr)
	}
	subnet, _ := body["subnet"].(string)
	p, err := netip.ParsePrefix(subnet)
	if err != nil {
		t.Fatalf("the subnet became %q, which is not a prefix", subnet)
	}
	if p.Bits() != 22 {
		t.Errorf("the subnet became a /%d where the recording had a /22: a client computing with it gets another answer", p.Bits())
	}
	if p.Masked() != p {
		t.Errorf("the subnet %q is not a network address", subnet)
	}
	if _, err := time.Parse(time.RFC3339, body["created_at"].(string)); err != nil {
		t.Errorf("the timestamp became %q, which does not parse", body["created_at"])
	}
	// Numbers and booleans are kept: they are what the emulator validates a
	// request against, and no identifier of these dialects is one.
	if size, _ := body["size"].(json.Number); size.String() != "10000000000" {
		t.Errorf("the size became %v; a replayed request would then ask for another volume", body["size"])
	}
	if body["protected"] != true {
		t.Errorf("the boolean became %v", body["protected"])
	}
}

// The same value gets the same replacement, everywhere it appears.
//
// This is what makes a sanitised transcript replayable at all: the identifier a
// create answered has to be the identifier the read that follows addresses, or
// the causality of the recording is broken and every read answers 404.
func TestOneValueGetsOneReplacementThroughout(t *testing.T) {
	out, _ := sanitise(t, recording())
	created, _ := out[0].Res.Body.(map[string]any)["id"].(string)
	read, _ := out[1].Res.Body.(map[string]any)["id"].(string)
	if created != read {
		t.Errorf("the create answered %q and the read answered %q: the transcript no longer refers to itself", created, read)
	}
	if !strings.HasSuffix(out[1].Path, "/"+created) {
		t.Errorf("the read addresses %q, which is not the object the create answered", out[1].Path)
	}
	// The name the request sent comes back in the answer, which is what an
	// emulator.InvariantValue declaration compares.
	req, _ := out[0].Req.Body.(map[string]any)
	res, _ := out[0].Res.Body.(map[string]any)
	if req["name"] != res["name"] {
		t.Errorf("the request named %v and the answer names %v: a value invariant would report a divergence nobody caused",
			req["name"], res["name"])
	}
}

// A value the provider's own document enumerates stays as it is.
//
// Measured, not designed: without it the first real Scaleway corpus replayed
// four list operations as 400, because "order_by=created_at_desc" had become a
// synthetic string and the emulator refuses an order it does not know. A 400
// the sanitiser manufactured reads exactly like an emulator defect, which is
// the "artefact of the instrument" family this chain exists to keep out.
func TestAnEnumeratedValueSurvivesSanitisation(t *testing.T) {
	out, _ := sanitise(t, recording())
	if !strings.Contains(out[1].Query, "order_by=created_at_desc") {
		t.Errorf("the query became %q: the enumerated order did not survive", out[1].Query)
	}
	if !strings.Contains(out[1].Query, "page=1") {
		t.Errorf("the query became %q: a page number is not an identifier and must survive", out[1].Query)
	}
}

// A zone a pack vouches for stays; every other segment of the path is decided
// by the document.
func TestThePathKeepsItsLiteralsAndLosesItsIdentifiers(t *testing.T) {
	out, _ := sanitise(t, recording())
	if want := "/thing/v1/zones/fr-par-1/things"; out[0].Path != want {
		t.Errorf("the create's path became %q, want %q", out[0].Path, want)
	}
	if strings.Contains(out[1].Path, "6d4c02be") {
		t.Errorf("the read's path still carries the account's identifier: %s", out[1].Path)
	}
}

// A path the document does not describe loses every segment, and says so.
//
// Default deny where it costs something: a bucket name, a DNS zone and a
// project slug are all "just a word" in a path, and a tool that kept the words
// it did not recognise would publish exactly those.
func TestAPathTheDocumentDoesNotDescribeIsBlankedEntirely(t *testing.T) {
	exs := []trace.Exchange{{
		Method: "GET", Path: "/object/v1/my-company-backups/dump.sql", Status: 200,
		Res: &trace.Message{Body: map[string]any{}},
	}}
	out, rep := sanitise(t, exs)
	for _, gone := range []string{"my-company-backups", "dump.sql", "object"} {
		if strings.Contains(out[0].Path, gone) {
			t.Errorf("the blanked path still carries %q: %s", gone, out[0].Path)
		}
	}
	if len(rep.Unnamed) != 1 {
		t.Errorf("the report names %d unnamed path(s), want 1: a loss that is not counted is a loss nobody knows about", len(rep.Unnamed))
	}
	if strings.Count(out[0].Path, "/") != strings.Count(exs[0].Path, "/") {
		t.Errorf("the blanked path has a different depth: %s", out[0].Path)
	}
}

// A long run of digits is an identifier, not a page number.
//
// Outscale mints a twelve-digit account number as a string, so "all digits"
// without a bound would be a rule that publishes account numbers.
func TestALongRunOfDigitsIsNotKept(t *testing.T) {
	exs := []trace.Exchange{{
		Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
		Res: &trace.Message{Body: map[string]any{"account": "123456789012", "page": "12"}},
	}}
	out, _ := sanitise(t, exs)
	body, _ := out[0].Res.Body.(map[string]any)
	if body["account"] == "123456789012" {
		t.Error("a twelve-digit account number was kept verbatim")
	}
	if body["page"] != "12" {
		t.Errorf("a page number became %v; a replayed request would then ask for another page", body["page"])
	}
}

// An OpenSSH public key becomes an OpenSSH public key.
//
// Not decoration: internal/core/sshkey refuses anything that is not one, at the
// entry of both packs that accept a key, so a key replaced by a bare token is a
// create the emulator answers 400 to and a divergence nobody caused.
func TestAPublicKeyIsReplacedByAValidPublicKey(t *testing.T) {
	const real = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1J6cKvS0jVJdM3pQ5Xz9mFqQ0oQnQ0zZ0aVQxYcWzE somebody@station"
	exs := []trace.Exchange{{
		Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
		Req: &trace.Message{Body: map[string]any{"public_key": real}},
		Res: &trace.Message{Body: map[string]any{"public_key": real}},
	}}
	out, _ := sanitise(t, exs)
	got, _ := out[0].Req.Body.(map[string]any)["public_key"].(string)
	if got == real {
		t.Fatal("the key was kept verbatim")
	}
	if !sshkey.Valid(got) {
		t.Errorf("the key became %q, which sshkey.Parse refuses: every create carrying it would answer 400", got)
	}
	if strings.Contains(got, "somebody@station") {
		t.Errorf("the comment survived: %q", got)
	}
}

// The client stays nameable, and the agent string does not survive.
func TestTheClientOfASanitisedExchangeIsStillNamed(t *testing.T) {
	out, _ := sanitise(t, recording())
	agent := out[0].Req.Headers["User-Agent"]
	if strings.Contains(agent, "2.56.3") {
		t.Errorf("the agent kept what the build put in it: %q", agent)
	}
	if got := transcript.ClientOf(&out[0]); got != transcript.ClientSCW {
		t.Errorf("the sanitised exchange is attributed to %q, want %q: the ranking of what a real client called would lose its column",
			got, transcript.ClientSCW)
	}
}

// Two runs over one recording produce the same bytes.
//
// The rule internal/shape states for a committed artefact: the drift workflow
// decides something changed with `git diff --quiet`, so a file that moves on
// its own turns the signal into noise.
func TestSanitisingTwiceProducesTheSameBytes(t *testing.T) {
	first, _ := sanitise(t, recording())
	second, _ := sanitise(t, recording())
	a, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("two runs differ:\n%s\n%s", a, b)
	}
	if first[0].At != corpusEpoch {
		t.Errorf("the first exchange is stamped %v, want the fixed epoch: a recording's own clock says when its author was working", first[0].At)
	}
	if first[0].Ms != 0 {
		t.Errorf("the exchange kept its duration (%v): a value that moves every run makes a committed artefact noise", first[0].Ms)
	}
}

// A value read from the recording that survives into the artefact is named.
//
// The guard is [Audit], and it does not depend on the sanitiser being right
// about what a value *is*: the only question asked is "was this string in the
// recording", which is what catches the shape a fourth provider invents.
func TestAValueOfTheRecordingThatSurvivesIsNamed(t *testing.T) {
	exs := recording()
	out, _ := sanitise(t, exs)
	if leaks := Audit(exs, out, options(t)); len(leaks) != 0 {
		t.Fatalf("a clean sanitisation is reported as leaking, which would make the control noise: %v", leaks)
	}

	// One value put back, the way a sanitiser with one rule missing leaves it.
	out[1].Res.Body.(map[string]any)["id"] = "6d4c02be-9a15-4f83-8b7d-2e91c40fa5d8"
	leaks := Audit(exs, out, options(t))
	if len(leaks) == 0 {
		t.Fatal("an identifier of the recording survived into the artefact and the audit said nothing")
	}
	if strings.Contains(leaks[0].String(), "6d4c02be") {
		t.Errorf("the audit republished the value it was refusing: %s", leaks[0])
	}
}

// The alphabet refuses a value it did not mint, wherever it sits.
func TestScanRefusesAValueOutsideTheAlphabet(t *testing.T) {
	out, _ := sanitise(t, recording())
	if leaks := Scan(out, options(t)); len(leaks) != 0 {
		t.Fatalf("a sanitised transcript is refused by its own alphabet: %v", leaks)
	}
	out[0].Res.Body.(map[string]any)["name"] = "production-database"
	leaks := Scan(out, options(t))
	if len(leaks) == 0 {
		t.Fatal("a value no rule of this package could have produced passed the scan")
	}
	if leaks[0].Where != "res.body.name" {
		t.Errorf("the leak is reported at %q, want res.body.name", leaks[0].Where)
	}
}

// Sanitising without the provider's document is refused rather than guessed at.
func TestSanitisingWithoutAContractIsRefused(t *testing.T) {
	if _, _, err := Sanitise(recording(), Options{}); err == nil {
		t.Fatal("a recording was sanitised with no document to say which path segments are the API's own")
	}
}

// Two values of the recording never become one value of the artefact.
//
// The property is what a replay rests on and what a reader of the corpus reads
// off it: "these two networks are the same block" has to mean it. The first
// version of the IPv6 arithmetic broke it silently — every /64 of a recording
// became 2001:db8::/64, because the counter was placed in the bits a /64 mask
// erases — and the finding it would have manufactured is #270's own ("two
// networks of one project share a /48"), the instrument inventing the
// measurement.
func TestTwoValuesNeverShareAReplacement(t *testing.T) {
	values := []string{
		"3f2a91c4-77b0-4d19-9c2e-51ab8e0d64f7", "6d4c02be-9a15-4f83-8b7d-2e91c40fa5d8",
		"51.15.0.1", "51.15.0.2", "2001:4860:4860::8888", "2001:4860:4860::8844",
		"172.16.32.0/22", "172.16.36.0/22", "10.0.0.0/8",
		"2a02:1:2:3::/64", "2a02:1:2:4::/64", "2a02:1::/48", "2a02:2::/48",
		"production-database", "staging-database",
		"2026-08-21T09:12:33Z", "2026-08-21T09:12:34Z",
		"i-0e4a3c1f", "i-0e4a3c20",
	}
	body := map[string]any{}
	for i, v := range values {
		body[fmt.Sprintf("f%02d", i)] = v
	}
	out, _, err := Sanitise([]trace.Exchange{{
		Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
		Res: &trace.Message{Body: body},
	}}, options(t))
	if err != nil {
		t.Fatalf("the sanitisation refused its own output: %v", err)
	}
	got, _ := out[0].Res.Body.(map[string]any)
	seen := map[string]string{}
	for field, v := range got {
		s, _ := v.(string)
		if first, twice := seen[s]; twice {
			t.Errorf("%s and %s were both replaced by %q: two values of the account read as one", first, field, s)
		}
		seen[s] = field
	}
	if len(seen) != len(values) {
		t.Errorf("%d distinct replacements for %d distinct values", len(seen), len(values))
	}
	// The prefix lengths are the contract of the block, and they survive.
	for field, v := range got {
		s, _ := v.(string)
		if !strings.Contains(s, "/") {
			continue
		}
		want, err := netip.ParsePrefix(values[mustIndex(t, field)])
		if err != nil {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Bits() != want.Bits() {
			t.Errorf("%s: /%d became %q", field, want.Bits(), s)
		}
	}
}

// mustIndex reads back the position a field name encodes.
func mustIndex(t *testing.T, field string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimPrefix(field, "f"))
	if err != nil {
		t.Fatalf("field %q does not carry its index", field)
	}
	return n
}

// The prefix that selects every address survives, and the audit accepts it.
//
// Measured before it was written: recording `exo compute security-group rule
// add --network 0.0.0.0/0` against a real Exoscale account on 2026-08-21 made
// `--sanitise` refuse the whole run at eleven positions, every one of them this
// value. [mint.cidr] cannot replace it — masking a zero-length prefix yields
// the same prefix — so without the sixth reason in [mint.keepable] the most
// ordinary rule a cloud answers is unrecordable, and the only way past is to
// trim the corpus until it passes.
//
// The second half is what keeps the reason narrow: a prefix whose address half
// still carries something of the account is replaced like any other.
func TestTheDefaultRouteSurvivesSanitisation(t *testing.T) {
	exs := []trace.Exchange{{
		Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
		Req: &trace.Message{Body: map[string]any{"network": "0.0.0.0/0", "v6": "::/0"}},
		Res: &trace.Message{Body: map[string]any{
			"network": "0.0.0.0/0", "v6": "::/0", "unmasked": "10.0.0.0/0", "subnet": "172.16.32.0/22",
		}},
	}}
	out, _ := sanitise(t, exs)
	res, _ := out[0].Res.Body.(map[string]any)
	req, _ := out[0].Req.Body.(map[string]any)

	for where, body := range map[string]map[string]any{"req": req, "res": res} {
		if body["network"] != "0.0.0.0/0" {
			t.Errorf("%s: the default route became %v; a replayed rule would then open a different network",
				where, body["network"])
		}
		if body["v6"] != "::/0" {
			t.Errorf("%s: the IPv6 default route became %v", where, body["v6"])
		}
	}
	if res["unmasked"] == "10.0.0.0/0" {
		t.Error("a /0 whose address half is the account's was kept verbatim")
	}
	if res["subnet"] == "172.16.32.0/22" {
		t.Error("an ordinary CIDR was kept verbatim")
	}

	if leaks := Audit(exs, out, options(t)); len(leaks) != 0 {
		t.Fatalf("the audit refused a transcript whose only surviving value is the default route: %v", leaks)
	}
	if leaks := Scan(out, options(t)); len(leaks) != 0 {
		t.Fatalf("the alphabet refused the default route: %v", leaks)
	}
}

// A dotted netmask is replaced by a dotted netmask, and never by itself.
//
// Measured before it was written. Replaying the real Exoscale recording of
// 2026-08-21, `netmask: "255.255.255.0"` had become an address of the synthetic
// space, the emulator answered `400 netmask is not a usable IPv4 netmask` where
// the cloud answered 200, and the get, the update, the delete and the three
// operation polls that followed all answered 404 for that one reason: around
// twenty findings, not one of them a defect of the emulator.
//
// Both halves matter and neither implies the other. Keeping the shape is what
// stops the instrument manufacturing a divergence; keeping the *value* out is
// what the whole package is for, and "255.255.255.0" is still a value the
// account sent.
func TestANetmaskIsReplacedByANetmask(t *testing.T) {
	exs := []trace.Exchange{{
		Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
		Req: &trace.Message{Body: map[string]any{"netmask": "255.255.255.0"}},
		Res: &trace.Message{Body: map[string]any{
			"netmask": "255.255.255.0", "wide": "255.255.0.0", "host": "10.90.1.20", "any": "0.0.0.0",
		}},
	}}
	out, _ := sanitise(t, exs)
	res, _ := out[0].Res.Body.(map[string]any)
	req, _ := out[0].Req.Body.(map[string]any)

	for _, got := range []any{req["netmask"], res["netmask"], res["wide"]} {
		mask, _ := got.(string)
		if netmaskBits(mask) == 0 {
			t.Errorf("a netmask became %q, which is not one: the create that carries it is refused 400 and every read after it 404s", mask)
		}
	}
	if req["netmask"] == "255.255.255.0" || res["netmask"] == "255.255.255.0" {
		t.Error("the account's own netmask was written down verbatim")
	}
	if req["netmask"] != res["netmask"] {
		t.Errorf("one netmask got two replacements, %v and %v", req["netmask"], res["netmask"])
	}
	if res["netmask"] == res["wide"] {
		t.Errorf("two netmasks share a replacement (%v): the transcript says two networks were one", res["netmask"])
	}
	// A host address is not a mask, and "0.0.0.0" is not one either: both go
	// through the address mint, into the synthetic space.
	for _, name := range []string{"host", "any"} {
		addr, err := netip.ParseAddr(res[name].(string))
		if err != nil || !syntheticV4.Contains(addr) {
			t.Errorf("%s became %v, which is not an address of the synthetic space", name, res[name])
		}
	}

	if leaks := Audit(exs, out, options(t)); len(leaks) != 0 {
		t.Fatalf("the audit refused a clean sanitisation: %v", leaks)
	}
	if leaks := Scan(out, options(t)); len(leaks) != 0 {
		t.Fatalf("the alphabet refused a minted netmask: %v", leaks)
	}
}

// Two addresses that bound a range still bound one after sanitisation.
//
// Measured before it was written. Replaying the real Exoscale recording of
// 2026-08-21, `start-ip` and `end-ip` were minted in the order the walk met
// them — "end-ip" sorts before "start-ip" — so the artefact carried a range
// running backwards and the emulator answered `400 end-ip is below start-ip`
// where the cloud answered 200. Everything the create was meant to make then
// answered 404: around twenty findings, not one a defect of the emulator.
//
// The rule names no field: it is the addresses that are sorted, so "start"/
// "end", "first"/"last" and whatever a fourth provider calls them are one
// problem with one answer.
func TestAnAddressRangeStillRunsForwards(t *testing.T) {
	exs := []trace.Exchange{{
		Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
		// The field names are deliberately in an order that sorts the wrong way.
		Req: &trace.Message{Body: map[string]any{
			"end-ip": "10.90.1.200", "start-ip": "10.90.1.20", "gateway": "10.90.1.1",
		}},
		Res: &trace.Message{Body: map[string]any{"v6end": "2001:41d0::20", "v6start": "2001:41d0::2"}},
	}}
	out, _ := sanitise(t, exs)
	req, _ := out[0].Req.Body.(map[string]any)
	res, _ := out[0].Res.Body.(map[string]any)

	order := func(t *testing.T, lower, upper any, what string) {
		t.Helper()
		lo, err := netip.ParseAddr(lower.(string))
		if err != nil {
			t.Fatalf("%s: %v is not an address", what, lower)
		}
		hi, err := netip.ParseAddr(upper.(string))
		if err != nil {
			t.Fatalf("%s: %v is not an address", what, upper)
		}
		if !lo.Less(hi) {
			t.Errorf("%s: the range now runs %v..%v, backwards; the create that carries it is refused 400 and everything it would have made 404s",
				what, lo, hi)
		}
	}
	order(t, req["gateway"], req["start-ip"], "gateway below start")
	order(t, req["start-ip"], req["end-ip"], "start below end")
	order(t, res["v6start"], res["v6end"], "IPv6 start below end")

	for _, gone := range []string{"10.90.1.200", "10.90.1.20", "10.90.1.1", "2001:41d0::20", "2001:41d0::2"} {
		if raw, _ := json.Marshal(out); bytes.Contains(raw, []byte(gone)) {
			t.Errorf("the ordering was kept by keeping the value itself: %q survived", gone)
		}
	}
	if leaks := Audit(exs, out, options(t)); len(leaks) != 0 {
		t.Fatalf("the audit refused a clean sanitisation: %v", leaks)
	}
}

// Every synthetic address and every synthetic block stays inside 198.18.0.0/15.
//
// Measured before it was written. Outscale's `ReadPublicIpRanges` publishes the
// provider's whole public address space — 90 blocks on 2026-08-21, three /20s
// among 79 /24s — and one counter shared by every prefix length shifted a
// two-digit counter twelve bits and landed in 198.20.0.0. The alphabet refused
// the artefact, which is the right outcome, but it named the wrong thing: the
// message said "not a value a sanitised transcript may carry" where the fault
// was arithmetic four functions away.
//
// Two halves, and the second is what makes the first true at this size: the
// counter is per prefix length, and [offsetV4] confines whatever it is given.
// An exhausted space then repeats a replacement, and [Sanitise] refuses that by
// name — a corpus in which two blocks of an account read as one is the failure
// #270 found by hand, and it must never be manufactured here.
func TestASyntheticAddressStaysInTheSyntheticSpace(t *testing.T) {
	body := map[string]any{}
	// Three /20s and eighty /24s, the shape ReadPublicIpRanges answers.
	var blocks []any
	for i := 0; i < 3; i++ {
		blocks = append(blocks, fmt.Sprintf("10.%d.0.0/20", i+1))
	}
	for i := 0; i < 80; i++ {
		blocks = append(blocks, fmt.Sprintf("172.16.%d.0/24", i))
	}
	body["PublicIps"] = blocks
	exs := []trace.Exchange{{
		Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
		Res: &trace.Message{Body: body},
	}}
	out, _ := sanitise(t, exs)

	got, _ := out[0].Res.Body.(map[string]any)["PublicIps"].([]any)
	if len(got) != len(blocks) {
		t.Fatalf("%d block(s) came back for %d", len(got), len(blocks))
	}
	seen := map[string]bool{}
	for _, raw := range got {
		s, _ := raw.(string)
		p, err := netip.ParsePrefix(s)
		if err != nil {
			t.Fatalf("%q is not a prefix", s)
		}
		if !syntheticV4.Overlaps(p) || !syntheticV4.Contains(p.Addr()) {
			t.Errorf("%q is outside %s, which is the only IPv4 space a sanitised transcript may carry", s, syntheticV4)
		}
		if seen[s] {
			t.Errorf("%q was handed out twice: two blocks of the recording read as one", s)
		}
		seen[s] = true
	}
	if leaks := Scan(out, options(t)); len(leaks) != 0 {
		t.Fatalf("the alphabet refused the artefact: %v", leaks)
	}
}

// A synthetic space with no room left refuses the run; it never writes outside
// itself.
//
// 198.18.0.0/15 holds 512 /24s and no more, so a recording carrying more of
// them has no shape-preserving sanitisation at all. The two ways that can end
// are not equivalent: an address minted past the end of the block is a value
// [Scan] refuses with "not a value a sanitised transcript may carry", which
// names where it landed and not what happened, while a wrap repeats a
// replacement and [Sanitise] refuses that by name — and a corpus in which two
// blocks of an account read as one is the finding #270 made by hand, which this
// package must never manufacture.
//
// TestASyntheticAddressStaysInTheSyntheticSpace holds the ordinary size;
// this holds the edge, and both need [offsetV4]'s confinement.
func TestASpaceWithNoRoomLeftIsRefusedRatherThanOverrun(t *testing.T) {
	var blocks []any
	for i := 0; i < 600; i++ {
		blocks = append(blocks, fmt.Sprintf("172.%d.%d.0/24", 16+i/256, i%256))
	}
	exs := []trace.Exchange{{
		Method: "POST", Path: "/thing/v1/zones/fr-par-1/things", Status: 200,
		Res: &trace.Message{Body: map[string]any{"PublicIps": blocks}},
	}}
	out, _, err := Sanitise(exs, options(t))
	if err == nil {
		t.Fatalf("600 /24s were sanitised into a space that holds 512: the artefact carries %d block(s) and cannot be right",
			len(out[0].Res.Body.(map[string]any)["PublicIps"].([]any)))
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("the refusal does not say a replacement was handed out twice: %v", err)
	}
}

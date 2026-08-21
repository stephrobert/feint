package outscale_test

import (
	"net/http"
	"testing"
)

// The DHCP options lifecycle (#172, second tranche). ReadDhcpOptions and the
// account's default set predate these tests (netdefaults_test.go holds them);
// what is asserted here is what a client can now do on top: create a second
// set, point a Net at it, and be refused the two deletes the real cloud
// refuses. Each refusal is asserted beside the accepting half, because a guard
// that refuses everything passes every attack test and breaks the product.

func TestCreateDhcpOptionsRoundTrips(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateDhcpOptions",
		`{"DomainName":"feint.example","DomainNameServers":["192.0.2.53","192.0.2.54"],"NtpServers":["192.0.2.123"]}`)
	set, _ := created["DhcpOptionsSet"].(map[string]any)
	id, _ := set["DhcpOptionsSetId"].(string)
	if id == "" || len(id) != len("dopt-")+8 || id[:5] != "dopt-" {
		t.Fatalf("DhcpOptionsSetId = %q, want a dopt- id with an 8-hex suffix", id)
	}
	if def, _ := set["Default"].(bool); def {
		t.Fatalf("a created set claims to be the account's default: %v", set)
	}
	// LogServers was not sent: omitted, not empty, the way the measured
	// default set behaves.
	if _, present := set["LogServers"]; present {
		t.Fatalf("LogServers was not requested and is present anyway: %v", set)
	}

	// Whatever the create returned, the following read returns identically —
	// the single most common cause of a permanent Terraform diff, and the
	// provider reads back by exactly this filter.
	read := call(t, ts, doc, "ReadDhcpOptions",
		`{"Filters":{"DhcpOptionsSetIds":["`+id+`"]}}`)
	sets, _ := read["DhcpOptionsSets"].([]any)
	if len(sets) != 1 {
		t.Fatalf("the created set does not read back by its id: %v", read)
	}
	got, _ := sets[0].(map[string]any)
	if got["DomainName"] != "feint.example" {
		t.Errorf("DomainName = %v, want feint.example", got["DomainName"])
	}
	servers, _ := got["DomainNameServers"].([]any)
	if len(servers) != 2 || servers[0] != "192.0.2.53" || servers[1] != "192.0.2.54" {
		t.Errorf("DomainNameServers = %v, want the two sent, in order", got["DomainNameServers"])
	}
	ntp, _ := got["NtpServers"].([]any)
	if len(ntp) != 1 || ntp[0] != "192.0.2.123" {
		t.Errorf("NtpServers = %v, want the one sent", got["NtpServers"])
	}
}

// "If no IPs are specified, the OutscaleProvidedDNS value is set by default" —
// the keyword, exactly as the account's default set carries it.
func TestCreateDhcpOptionsDefaultsItsDNSKeyword(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateDhcpOptions", `{"DomainName":"feint.example"}`)
	set, _ := created["DhcpOptionsSet"].(map[string]any)
	servers, _ := set["DomainNameServers"].([]any)
	if len(servers) != 1 || servers[0] != "OutscaleProvidedDNS" {
		t.Fatalf("DomainNameServers = %v, want the OutscaleProvidedDNS keyword", set["DomainNameServers"])
	}
}

// At least one of the four options, which their document requires on each
// field. Without the guard an empty create stores a set that configures
// nothing; this test is the one the falsification spec names.
func TestCreateDhcpOptionsRequiresAtLeastOneOption(t *testing.T) {
	ts := newServer(t)

	status, refused := post(t, ts, "CreateDhcpOptions", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an empty create answered %d (%v), want 400", status, refused)
	}
	// And nothing was stored: only the lazily created default set may exist.
	_, read := post(t, ts, "ReadDhcpOptions", `{}`)
	sets, _ := read["DhcpOptionsSets"].([]any)
	for _, raw := range sets {
		set, _ := raw.(map[string]any)
		if def, _ := set["Default"].(bool); !def {
			t.Fatalf("the refused create left a set behind: %v", set)
		}
	}
}

// "You cannot delete the `default` set" — their document, under
// DeleteDhcpOptions. The falsification spec names this test.
func TestTheDefaultDhcpOptionsSetDoesNotDelete(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	read := call(t, ts, doc, "ReadDhcpOptions", `{}`)
	def := firstOf(t, read, "DhcpOptionsSets")
	id, _ := def["DhcpOptionsSetId"].(string)

	status, refused := post(t, ts, "DeleteDhcpOptions", `{"DhcpOptionsSetId":"`+id+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("deleting the default set answered %d (%v), want 409", status, refused)
	}

	// Still there, still the default.
	after := call(t, ts, doc, "ReadDhcpOptions", `{"Filters":{"DhcpOptionsSetIds":["`+id+`"]}}`)
	if sets, _ := after["DhcpOptionsSets"].([]any); len(sets) != 1 {
		t.Fatalf("the refused delete removed the default set anyway: %v", after)
	}
}

// "Before deleting a DHCP options set, you must disassociate it from the Nets
// you associated it with" — and the disassociation the provider performs is
// UpdateNet with the `default` keyword, so both halves are driven here in the
// provider's own order. The falsification spec names this test.
func TestADhcpOptionsSetDoesNotDeleteUnderANet(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateDhcpOptions", `{"NtpServers":["192.0.2.123"]}`)
	set, _ := created["DhcpOptionsSet"].(map[string]any)
	setID, _ := set["DhcpOptionsSetId"].(string)

	madeNet := call(t, ts, doc, "CreateNet", `{"IpRange":"10.40.0.0/16"}`)
	net, _ := madeNet["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)
	defaultID, _ := net["DhcpOptionsSetId"].(string)

	call(t, ts, doc, "UpdateNet", `{"NetId":"`+netID+`","DhcpOptionsSetId":"`+setID+`"}`)

	status, refused := post(t, ts, "DeleteDhcpOptions", `{"DhcpOptionsSetId":"`+setID+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("deleting a set a Net wears answered %d (%v), want 409", status, refused)
	}

	// Detached the way the provider detaches: the `default` keyword, resolved
	// to the account's set rather than stored verbatim — what a Net carries is
	// always a dopt- id.
	updated := call(t, ts, doc, "UpdateNet", `{"NetId":"`+netID+`","DhcpOptionsSetId":"default"}`)
	after, _ := updated["Net"].(map[string]any)
	if got, _ := after["DhcpOptionsSetId"].(string); got != defaultID {
		t.Fatalf("the default keyword stored %q, want the account's set %s", got, defaultID)
	}

	// Now the delete goes, and the set is gone from every read.
	call(t, ts, doc, "DeleteDhcpOptions", `{"DhcpOptionsSetId":"`+setID+`"}`)
	read := call(t, ts, doc, "ReadDhcpOptions", `{"Filters":{"DhcpOptionsSetIds":["`+setID+`"]}}`)
	if sets, _ := read["DhcpOptionsSets"].([]any); len(sets) != 0 {
		t.Fatalf("the deleted set still answers: %v", read)
	}

	// The refusing half of the identifier space: a delete naming nothing is
	// this pack's 400 InvalidResource, never a 404 (errors.go).
	status, refused = post(t, ts, "DeleteDhcpOptions", `{"DhcpOptionsSetId":"`+setID+`"}`)
	if status != http.StatusBadRequest || !refusesUnknownResource(refused) {
		t.Errorf("a gone set answered %d (%v), want 400 InvalidResource", status, refused)
	}
	status, _ = post(t, ts, "DeleteDhcpOptions", `{}`)
	if status != http.StatusBadRequest {
		t.Errorf("a delete without an id answered %d, want 400", status)
	}
}

// The filter the provider's delete path walks (getAttachedDHCPs), asserted on
// a value that exists and one that does not: a filter that always matches and
// one that never matches are equally useless.
func TestReadNetsFiltersOnTheDhcpOptionsSet(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateDhcpOptions", `{"DomainName":"feint.example"}`)
	set, _ := created["DhcpOptionsSet"].(map[string]any)
	setID, _ := set["DhcpOptionsSetId"].(string)

	madeWorn := call(t, ts, doc, "CreateNet", `{"IpRange":"10.41.0.0/16"}`)
	worn, _ := madeWorn["Net"].(map[string]any)
	wornID, _ := worn["NetId"].(string)
	call(t, ts, doc, "CreateNet", `{"IpRange":"10.42.0.0/16"}`)

	call(t, ts, doc, "UpdateNet", `{"NetId":"`+wornID+`","DhcpOptionsSetId":"`+setID+`"}`)

	read := call(t, ts, doc, "ReadNets", `{"Filters":{"DhcpOptionsSetIds":["`+setID+`"]}}`)
	nets, _ := read["Nets"].([]any)
	if len(nets) != 1 {
		t.Fatalf("the filter matched %d Nets, want exactly the one wearing the set: %v", len(nets), read)
	}
	if got, _ := nets[0].(map[string]any); got["NetId"] != wornID {
		t.Fatalf("the filter answered %v, want %s", got["NetId"], wornID)
	}

	none := call(t, ts, doc, "ReadNets", `{"Filters":{"DhcpOptionsSetIds":["dopt-00000000"]}}`)
	if nets, _ := none["Nets"].([]any); len(nets) != 0 {
		t.Fatalf("an unknown set matched %d Nets, want none", len(nets))
	}
}

// A name server is an address, and the cloud refuses anything else before it
// stores a set. Measured on 2026-08-21 against a real account
// (corpus/outscale/oapi-cli-refusals.jsonl): CreateDhcpOptions with
// DomainNameServers ["not-an-address"] answered 400 InvalidParameterValue,
// where this pack answered 200 and stored the string.
//
// This is the test the comment in createDhcpOptions names, and it fails
// without the loop: the create is accepted and the set reads back carrying a
// name server no machine can resolve.
func TestCreateDhcpOptionsRefusesAServerThatIsNotAnAddress(t *testing.T) {
	ts := newServer(t)

	status, refused := post(t, ts, "CreateDhcpOptions", `{"DomainNameServers":["not-an-address"]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a name server that is not an address answered %d (%v), want 400", status, refused)
	}
	// Nothing was stored: only the lazily created default set may exist.
	_, read := post(t, ts, "ReadDhcpOptions", `{}`)
	sets, _ := read["DhcpOptionsSets"].([]any)
	for _, raw := range sets {
		set, _ := raw.(map[string]any)
		if def, _ := set["Default"].(bool); !def {
			t.Fatalf("the refused create left a set behind: %v", set)
		}
	}
	// The keyword the platform answers on the default set is still accepted:
	// it is the one value of this field that is not an address.
	if status, body := post(t, ts, "CreateDhcpOptions", `{"DomainNameServers":["OutscaleProvidedDNS"]}`); status != http.StatusOK {
		t.Fatalf("the platform's own resolver keyword answered %d (%v), want 200", status, body)
	}
}

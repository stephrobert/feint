package machine

import (
	"errors"
	"testing"
)

// The wordings below are copied verbatim from the daemon, not written from
// memory: every one was provoked on the station on 2026-11-16, Incus 7.6
// (Zabbly, client and server), OVN 24.03.8, OVS 3.3.9, by the probe scripts of
// #979 (real `incus` commands on throwaway networks and containers named
// fnt979*). The client ran under a French locale, which translates the
// "Error:" prefix and nothing else; the text after it is the server's.
//
// The recognisers they feed are prose matchers, a last resort by their own
// admission. The day a release rewords one of these messages, the matching
// row here is the only thing that says the recogniser went deaf, because the
// unit tests elsewhere hold shorter, hand-written forms. The existing
// TestIsNotFoundStaysNarrow holds the refusing half.

// TestEveryMatchedWordingOfIncus76IsRecognised fails when a recogniser stops
// matching the exact text the reference runtime answers with.
func TestEveryMatchedWordingOfIncus76IsRecognised(t *testing.T) {
	recognisers := map[string]func(error) bool{
		"isNotFound":          isNotFound,
		"isNotRunning":        isNotRunning,
		"isBusy":              isBusy,
		"isTransientConflict": isTransientConflict,
		"isAlreadyExists":     isAlreadyExists,
		"balancerGone":        balancerGone,
	}
	cases := []struct {
		recogniser string
		said       string
		command    string
	}{
		{"isNotFound", `Error: Failed to fetch instance "fnt979none" in project "default": Instance not found`, "incus info fnt979none"},
		{"isNotFound", `Error: Network not found`, "incus network show fnt979none"},
		{"isNotFound", `Error: Network ACL not found`, "incus network acl show fnt979none"},
		{"isNotFound", `Error: Network forward not found`, "incus network forward show fnt979a 10.250.79.200"},
		{"isNotFound", `Error: Failed deleting peer: Failed loading target network: Network not found`, "incus network peer delete on a row whose target network is gone (#455)"},
		{"isNotRunning", `Error: Instance is not running`, "incus exec on a stopped instance"},
		{"isBusy", `Error: Failed to create instance update operation: Instance is busy running a "stop" operation`, "incus config set during a graceful stop"},
		{"isTransientConflict", `Error: Failed to create instance update operation: Instance is busy running a "stop" operation`, "incus config device set during a graceful stop"},
		{"isTransientConflict", `Error: ETag doesn't match: 87d4d2ce4c008b97a0bd52e25bf342e70400c76dbc83eb29bffb6841be8813be vs bogus`, `incus query -X PUT -H 'If-Match: "bogus"' /1.0/network-acls/…`},
		{"isTransientConflict", `Error: Failed to update device "eth0": Failed updating OVN port: Failed ensuring security ACLs are configured in OVN for instance: Failed creating port group "incus_acl98_net71" for security ACL "fnt979acl1" and network "fnt979a" setup: constraint violation: Transaction causes multiple rows in "Port_Group" table to have identical values (incus_acl98_net71) for index on column "name".`, "two NICs taking one fresh rule set concurrently (1 round in 6)"},
		{"isAlreadyExists", `Error: The network ACL already exists`, "incus network acl create, twice"},
		{"isAlreadyExists", `Error: Network "fnt979a" already exists`, "incus network create, twice"},
		{"balancerGone", `Error: Network load balancer not found`, "incus query /1.0/networks/fnt979a/load-balancers/10.250.79.201"},
		{"balancerGone", `Error: Failed deleting load balancer: Network load balancer not found`, "incus query -X DELETE /1.0/networks/fnt979a/load-balancers/10.250.79.201"},
	}
	for _, c := range cases {
		recognise := recognisers[c.recogniser]
		if !recognise(errors.New(c.said)) {
			t.Errorf("%s does not recognise what Incus 7.6 answered to %s:\n  %s", c.recogniser, c.command, c.said)
		}
	}
}

// TestTheIncus76WordingsThatMeanSomethingElseAreNotRetried holds the other half:
// two 7.6 answers that look like conflicts and are not. A retry on either would
// spin until the budget runs out on a refusal no retry can change.
func TestTheIncus76WordingsThatMeanSomethingElseAreNotRetried(t *testing.T) {
	for _, said := range []string{
		// A deleted rule set: #577, the ordering defect the network lock closes.
		`Error: Failed to start device "eth0": Cannot find security ACL ID for "iso-fnt-1234"`,
		// Measured on 2026-11-16: 7.5 and later refuse allow-stateless on any
		// rule set a bridge network or bridged NIC uses; asking again changes nothing.
		`Error: ACL is used by network "fnt979br": Action "allow-stateless" is only supported on OVN networks`,
	} {
		if isTransientConflict(errors.New(said)) {
			t.Errorf("isTransientConflict retries a refusal no retry can change:\n  %s", said)
		}
	}
}

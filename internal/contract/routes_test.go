package contract_test

import (
	"testing"

	"github.com/stephrobert/feint/internal/contract"
)

// A verb on a resource is spelled "{id}:action" by all three of these APIs, and
// the two halves answer to different owners: the parameter's name is the pack's,
// the action is the contract's.
//
// Comparing the whole segment worked only while both documents happened to name
// the parameter the same way. It stopped the day a route had to be mounted as
// {id}:revert-snapshot against a document writing {instance-id}:revert-snapshot,
// because one mux group carries one wildcard name.
func TestAnActionOnAParameterComparesTheActionAndNotTheName(t *testing.T) {
	cases := []struct {
		name       string
		want, got  string
		mismatched bool
	}{
		{"a different parameter name is the pack's business",
			"/v2/instance/{instance-id}:revert-snapshot", "/v2/instance/{id}:revert-snapshot", false},
		{"a different action is the contract's",
			"/v2/instance/{instance-id}:revert-snapshot", "/v2/instance/{id}:start", true},
		{"an action against no action at all",
			"/v2/instance/{instance-id}:revert-snapshot", "/v2/instance/{id}", true},
		{"a plain parameter still matches a plain parameter",
			"/v2/instance/{instance-id}", "/v2/instance/{id}", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := contract.SamePathForTest(c.want, c.got); got == c.mismatched {
				t.Errorf("comparing %q with %q reported same=%v", c.want, c.got, got)
			}
		})
	}
}

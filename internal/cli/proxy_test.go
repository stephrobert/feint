package cli

import (
	"bytes"
	"strings"
	"testing"
)

// The proxy refuses to listen off loopback unless asked.
//
// Its exposure is not serve's, and the message says so: what an open port here
// offers is a relay into a real cloud account and a transcript of whoever used
// it. Nothing about a browser applies.
//
// It drives the decision rather than the command, for the reason
// TestServeRefusesANonLoopbackAddress records: with the refusal removed, a test
// that ran the command would find it listening, and never return.
func TestProxyRefusesANonLoopbackAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:4600", ":4600", "[::]:4600", "192.168.1.10:4600"} {
		err := checkProxyAddr(addr, false)
		if err == nil {
			t.Errorf("%s was accepted", addr)
			continue
		}
		for _, want := range []string{"credential", "--expose-to-network"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal for %s does not mention %q: %v", addr, want, err)
			}
		}
	}

	// The accepting half.
	for _, addr := range []string{"127.0.0.1:4600", "localhost:4600", "[::1]:4600"} {
		if err := checkProxyAddr(addr, false); err != nil {
			t.Errorf("the loopback address %s was refused: %v", addr, err)
		}
	}
	if err := checkProxyAddr("0.0.0.0:4600", true); err != nil {
		t.Errorf("--expose-to-network did not lift the refusal: %v", err)
	}
}

// The command refuses what would leave nothing behind, and says which flag is
// missing.
//
// Every one of these returns before anything listens, which is what makes them
// safe to run in a test at all.
func TestProxyRefusesAnUnusableInvocation(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no upstream":       {[]string{"--record", "run.jsonl"}, "--upstream"},
		"no record":         {[]string{"--upstream", "https://api.scaleway.com"}, "--record"},
		"unknown provider":  {[]string{"--upstream", "https://api.scaleway.com", "--record", "-", "--provider", "aws"}, "unknown provider"},
		"unusable upstream": {[]string{"--upstream", "://", "--record", "-"}, "--upstream"},
		"exposed address": {[]string{
			"--upstream", "https://api.scaleway.com", "--record", "-", "--addr", "0.0.0.0:4600",
		}, "--expose-to-network"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := proxyCommand(tc.args, &stdout, &stderr); code != exitError {
				t.Fatalf("exit %d, expected %d", code, exitError)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("the refusal does not mention %q: %s", tc.want, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("a refusal wrote to stdout, where --record - puts the transcript: %q", stdout.String())
			}
		})
	}
}

// Naming is restricted to one pack when asked, and covers all of them by
// default.
//
// The default is what makes the issue's own command line right: the three
// providers do not collide in URL space, so a table built from all of them names
// every client's traffic and mislabels none of it.
func TestProxyNamesOperationsFromThePacksThemselves(t *testing.T) {
	all, err := proxyTable("")
	if err != nil {
		t.Fatalf("build the table: %v", err)
	}
	if len(all.All()) == 0 {
		t.Fatal("the default table is empty, so no transcript would name anything")
	}

	one, err := proxyTable("scaleway")
	if err != nil {
		t.Fatalf("build the scaleway table: %v", err)
	}
	if len(one.All()) >= len(all.All()) {
		t.Errorf("--provider scaleway named %d routes against %d for every pack: the filter does nothing",
			len(one.All()), len(all.All()))
	}
	for pattern, m := range one.All() {
		if m.Provider != "scaleway" {
			t.Errorf("--provider scaleway kept %s, owned by %s", pattern, m.Provider)
		}
	}
}

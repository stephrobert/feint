package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/proxy"
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
		err := checkProxyAddr(addr, false, false)
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
		if err := checkProxyAddr(addr, false, false); err != nil {
			t.Errorf("the loopback address %s was refused: %v", addr, err)
		}
	}
	if err := checkProxyAddr("0.0.0.0:4600", true, false); err != nil {
		t.Errorf("--expose-to-network did not lift the refusal: %v", err)
	}
}

// A forward proxy is refused off loopback, and --expose-to-network does not lift
// it.
//
// The escape hatch the reverse proxy has cannot be given to this one. A forward
// proxy holds an authority whose leaves a client has been told to trust, so a
// port open to the network is a machine that decrypts and files whatever anyone
// who can reach it sends — a credential-harvesting service, started by a flag.
// The reverse proxy off loopback is a relay to the one upstream its operator
// named, which is bad and bounded; this is neither.
//
// Both halves are asserted, because a refusal that also refused the legitimate
// case would pass the first half alone and make the mode unusable.
func TestAForwardProxyIsRefusedOffLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:4600", "192.168.1.10:4600", "127.0.0.1:4600"} {
		err := checkProxyAddr(addr, true, true)
		if err == nil {
			t.Errorf("--forward --expose-to-network on %s was accepted", addr)
			continue
		}
		for _, want := range []string{"--forward", "loopback"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal for %s does not mention %q: %v", addr, want, err)
			}
		}
	}
	// Off loopback without the flag is refused too, by the general guard.
	if err := checkProxyAddr("0.0.0.0:4600", false, true); err == nil {
		t.Error("a forward proxy on 0.0.0.0 without --expose-to-network was accepted")
	}
	// And the mode itself still works where it belongs.
	if err := checkProxyAddr("127.0.0.1:4600", false, true); err != nil {
		t.Errorf("a forward proxy on loopback was refused: %v", err)
	}
}

// The two doors are not passed together, and one of them is passed.
//
// --upstream sends every request to a host the operator chose; --forward sends
// each one where the client asked. An invocation carrying both has not said
// which, and a proxy that picked one silently would record the wrong endpoint
// while looking like it worked.
func TestProxyRefusesTwoDoorsAtOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		upstream, forward, intercept string
		want                         string
	}{
		"upstream and forward":  {"https://api.scaleway.com", "api.scaleway.com", "", "--forward"},
		"forward and intercept": {"", "api.scaleway.com", "api.scaleway.com", "--intercept"},
		"neither":               {"", "", "", "--upstream"},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkProxyMode(tc.upstream, tc.forward, tc.intercept)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not mention %q: %v", tc.want, err)
			}
		})
	}

	// The accepting half, one line per door, so a guard that refused everything
	// would fail here.
	for name, tc := range map[string]struct{ upstream, forward, intercept string }{
		"upstream alone":             {"https://api.scaleway.com", "", ""},
		"forward alone":              {"", "api.scaleway.com", ""},
		"upstream with intercept":    {"https://api.scaleway.com", "", "api.scaleway.com"},
		"forward with two hostnames": {"", "api.scaleway.com,*.exoscale.com", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkProxyMode(tc.upstream, tc.forward, tc.intercept); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
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
		// The CONNECT door gets no bypass of its own: the same three refusals
		// apply to it, and the exposure one applies harder.
		"forward with no record": {[]string{"--forward", "api.scaleway.com"}, "--record"},
		"forward off loopback": {[]string{
			"--forward", "api.scaleway.com", "--record", "-", "--addr", "0.0.0.0:4600",
		}, "--expose-to-network"},
		"forward exposed on purpose": {[]string{
			"--forward", "api.scaleway.com", "--record", "-", "--addr", "0.0.0.0:4600", "--expose-to-network",
		}, "loopback"},
		"forward and upstream": {[]string{
			"--forward", "api.scaleway.com", "--upstream", "https://api.scaleway.com", "--record", "-",
		}, "--forward"},
		"forward for nothing": {[]string{"--forward", " , ", "--record", "-"}, "no host to forward for"},
		"forward for everything": {[]string{
			"--forward", "*", "--record", "-", "--addr", "127.0.0.1:0",
		}, "every host"},
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

// The authority a recording run mints lives in a temporary file and nowhere
// else.
//
// This is #336's third security requirement, and the one that would be cheapest
// to get wrong: an interception CA installed into the system trust store
// outlives the run, and every process on the station then trusts certificates
// this proxy can mint for any name it is pointed at. So the file is temporary,
// it is removed when the command returns, and it is never one of the
// directories a distribution reads its roots from.
//
// The subject is asserted present before anything is asserted about it: a
// publishCA that wrote nothing at all would satisfy "not in the trust store"
// trivially, and that is the shape of green this repository has learned to
// distrust.
func TestTheInterceptionCAIsTemporaryAndNeverInstalled(t *testing.T) {
	authority, err := proxy.MintAuthority()
	if err != nil {
		t.Fatalf("mint the authority: %v", err)
	}
	path, cleanup, err := publishCA(authority)
	if err != nil {
		t.Fatalf("publish the CA: %v", err)
	}

	published, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the published CA is not readable at %s: %v", path, err)
	}
	if !bytes.Contains(published, []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("%s holds no certificate, so this test would pass on an empty file", path)
	}

	// Temporary, which is what makes it disposable.
	if dir := filepath.Dir(path); dir != filepath.Clean(os.TempDir()) {
		t.Errorf("the CA was written to %s, outside the temporary directory (%s): an interception "+
			"authority that lands somewhere durable outlives the run that needed it", dir, os.TempDir())
	}
	// And never where a distribution reads its roots from.
	for _, store := range []string{
		"/etc/ssl/certs", "/usr/local/share/ca-certificates", "/usr/share/ca-certificates",
		"/etc/pki/ca-trust", "/etc/ca-certificates",
	} {
		if strings.HasPrefix(path, store) {
			t.Errorf("the CA was written into the system trust store at %s", path)
		}
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s survived the run: the CA must go when the command that minted it returns", path)
	}
}

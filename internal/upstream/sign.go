package upstream

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Three dialects, one per provider, each written from that provider's own
// source rather than from documentation or memory. Where the file came from is
// recorded above each function, because the next person to touch one of these
// needs to know what to re-read when it stops working.

// signScaleway sets the token header. Scaleway signs nothing: the secret key is
// the credential, sent as-is, which is why `scw` only needs SCW_API_URL to be
// pointed elsewhere and why it is the one provider a proxy can carry without
// trouble.
func signScaleway(c credentials) signer {
	return func(req *http.Request, _ []byte) error {
		req.Header.Set("X-Auth-Token", c.secret)
		return nil
	}
}

// signOutscale implements OSC4-HMAC-SHA256.
//
// Verified against the real cloud rather than against a document: with the
// canonical URI set to the full request path, `ReadKeypairs` answers 200; with
// it set to "/" — which is what osc-cli's CANONICAL_URI constant uses — the same
// credential answers 401. Both were measured on
// api.cloudgouv-eu-west-1.outscale.com.
//
// The signature covers the Host header, which is why a client pointed at a proxy
// cannot reach the real cloud through it: the cloud recomputes from the Host it
// received. That is a boundary of any reverse proxy, not a defect, and lifting
// it needs DNS interception and TLS termination (#76).
//
// TestOutscaleSignatureCoversTheHost fails if the host stops being signed.
func signOutscale(c credentials) signer {
	return func(req *http.Request, body []byte) error {
		now := time.Now().UTC()
		stamp := now.Format("20060102T150405Z")
		day := now.Format("20060102")
		host := req.URL.Host

		canonical := strings.Join([]string{
			req.Method,
			req.URL.EscapedPath(),
			"", // no query string on these calls
			"content-type:application/json",
			"host:" + host,
			"x-osc-date:" + stamp,
			"",
			"content-type;host;x-osc-date",
			sha256hex(body),
		}, "\n")

		scope := strings.Join([]string{day, c.region, "api", "osc4_request"}, "/")
		toSign := strings.Join([]string{
			"OSC4-HMAC-SHA256", stamp, scope, sha256hex([]byte(canonical)),
		}, "\n")

		key := []byte("OSC4" + c.secret)
		for _, part := range []string{day, c.region, "api", "osc4_request"} {
			key = hmacSHA256(key, part)
		}

		req.Header.Set("X-Osc-Date", stamp)
		req.Header.Set("Authorization", fmt.Sprintf(
			"OSC4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=content-type;host;x-osc-date, Signature=%s",
			c.key, scope, hex.EncodeToString(hmacSHA256(key, toSign))))
		return nil
	}
}

// signExoscale implements EXO2-HMAC-SHA256.
//
// Written from egoscale's own v2/api/security.go, at the version this station
// has installed — the SDK the `exo` CLI is built on. The parts are joined by
// newlines in this order and no other: "METHOD escaped-path", the body, the
// concatenated values of the query parameters that carry exactly one value
// sorted by name, an empty line for signed headers (the SDK signs none), and
// the expiry as a UNIX timestamp.
//
// Nothing about the host is signed, unlike Outscale. What still stops a proxy
// from carrying `exo` is different and worth not confusing with it: the zone
// list the *server* returns carries an api-endpoint per zone, and the client
// follows it — so a recording against the real cloud loses the client after the
// first call.
//
// TestExoscaleSignatureMatchesTheSDKsOrder fails if the order changes.
func signExoscale(c credentials) signer {
	return func(req *http.Request, body []byte) error {
		expires := time.Now().UTC().Add(10 * time.Minute).Unix()

		var names []string
		query := req.URL.Query()
		for name, values := range query {
			if len(values) == 1 {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		var joined strings.Builder
		for _, name := range names {
			joined.WriteString(query.Get(name))
		}

		parts := []string{
			req.Method + " " + req.URL.EscapedPath(),
			string(body),
			joined.String(),
			"", // headers: the SDK signs none
			fmt.Sprint(expires),
		}
		mac := hmac.New(sha256.New, []byte(c.secret))
		mac.Write([]byte(strings.Join(parts, "\n")))

		header := []string{"EXO2-HMAC-SHA256 credential=" + c.key}
		if len(names) > 0 {
			header = append(header, "signed-query-args="+strings.Join(names, ";"))
		}
		header = append(header,
			"expires="+fmt.Sprint(expires),
			"signature="+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		req.Header.Set("Authorization", strings.Join(header, ","))
		return nil
	}
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

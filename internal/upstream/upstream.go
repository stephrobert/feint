// Package upstream talks to the real clouds, so that nothing else has to.
//
// It exists because the same three problems were solved from scratch every time
// somebody needed to ask a real provider a question: how to sign, how fast to
// ask, and what to do when the answer is a refusal that is not about the
// request. In one evening this repository accumulated **seven** copies of the
// Outscale signature in throwaway scripts, each subtly different, and the
// difference between two of them cost an hour of diagnosis.
//
// So: one place that knows how to sign for each provider, one place that paces,
// one place that retries. Everything else asks for an operation and gets a
// [trace.Exchange] — the shape the proxy already writes and `feint transcript`
// and `feint shapes` already read, so this adds no format to the project.
//
// # Read-only by construction
//
// [Client.Read] is the only way to make a call, and it refuses anything that is
// not a read for the provider in question. The guard sits where the request is
// built rather than in each caller, because a control a new caller can forget
// is a control that will be forgotten. Creating resources on a real account is
// deliberate work that belongs in a script somebody wrote on purpose, not a
// capability this package hands out.
//
// # Credentials
//
// Read from the same files the official CLIs use, at the moment of the call,
// and never written anywhere. Nothing here logs a key, and [trace.Exchange]
// records the response only — request headers are not captured at all, so a
// transcript cannot carry an Authorization line even by accident.
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/trace"
)

// Provider is one of the three clouds this project emulates.
type Provider string

const (
	Scaleway Provider = "scaleway"
	Outscale Provider = "outscale"
	Exoscale Provider = "exoscale"
)

// DefaultPace is how long to wait between two calls to the same provider.
//
// It is not politeness, it is a measurement. Firing thirty Outscale reads back
// to back made the API answer InvalidParameterValue 4120 on every
// *authenticated* call while the public ones kept working — which reads exactly
// like a revoked credential and is not one. The same calls, the same key, the
// same signature, spaced out, all answered 200.
//
// That failure mode is why this belongs in a shared client rather than in each
// script: a throttle that reports itself as a parameter error, on half the
// calls, sends a reader to look at their credentials for an hour. It did.
const DefaultPace = 400 * time.Millisecond

// Client asks one provider for what it returns.
type Client struct {
	Provider Provider
	// Endpoint is the base URL, without a trailing slash.
	Endpoint string
	// Pace is the gap between calls. Zero means DefaultPace; a negative value
	// means none, which exists for tests against a local server and should not
	// be used against a real cloud.
	Pace time.Duration
	// Retries is how many times a throttled call is tried again, with a
	// doubling wait. Zero means three.
	Retries int

	sign     signer
	http     *http.Client
	lastCall time.Time
}

// signer turns a request into an authenticated one. One implementation per
// provider, each written from that provider's own source rather than from a
// blog post: see sign_outscale.go and sign_exoscale.go for which file was read.
type signer func(req *http.Request, body []byte) error

// New builds a client for a provider, reading the operator's own credentials.
//
// It returns an error rather than a client that fails later: a missing profile
// is a thing to say once, at the point somebody can act on it, not thirty times
// in a loop.
func New(p Provider, profile string) (*Client, error) {
	creds, err := loadCredentials(p, profile)
	if err != nil {
		return nil, err
	}
	c := &Client{
		Provider: p,
		Endpoint: creds.endpoint,
		http:     &http.Client{Timeout: 60 * time.Second},
	}
	switch p {
	case Scaleway:
		c.sign = signScaleway(creds)
	case Outscale:
		c.sign = signOutscale(creds)
	case Exoscale:
		c.sign = signExoscale(creds)
	default:
		return nil, fmt.Errorf("unknown provider %q", p)
	}
	return c, nil
}

// ErrNotARead is returned for any call this package refuses to make.
var ErrNotARead = errors.New("upstream: refusing a call that is not a read")

// Read performs one read and returns it as a recorded exchange.
//
// call is what the provider calls the thing: an action name for Outscale
// ("ReadVms"), a path for Scaleway and Exoscale ("/instance/v1/zones/fr-par-1/
// servers"). The provider's own dialect is kept rather than invented, because a
// caller who knows the API should not have to learn a second naming.
//
// A refusal is returned as an exchange with its status, not as an error: what
// the cloud answered is the measurement, and a 400 carries a body worth
// recording. An error means the call could not be made or the answer could not
// be read.
func (c *Client) Read(ctx context.Context, call string) (trace.Exchange, error) {
	if !c.isRead(call) {
		return trace.Exchange{}, fmt.Errorf("%w: %s", ErrNotARead, call)
	}
	return c.do(ctx, call)
}

// isRead decides whether a call only reads, in each provider's own dialect.
//
// Outscale names an action, and every read of theirs starts with Read. Scaleway
// and Exoscale name a path and carry the verb separately, so a GET is the read
// and this package makes no other kind.
func (c *Client) isRead(call string) bool {
	if c.Provider == Outscale {
		return strings.HasPrefix(call, "Read")
	}
	return strings.HasPrefix(call, "/")
}

func (c *Client) do(ctx context.Context, call string) (trace.Exchange, error) {
	pace := c.Pace
	if pace == 0 {
		pace = DefaultPace
	}
	retries := c.Retries
	if retries == 0 {
		retries = 3
	}

	var last trace.Exchange
	wait := pace
	for attempt := 0; ; attempt++ {
		c.waitTurn(ctx, pace)

		ex, err := c.once(ctx, call)
		if err != nil {
			return trace.Exchange{}, err
		}
		last = ex

		// A throttle is retried; anything else is the answer. 429 is the
		// honest form, and Outscale's 400 is the one this package exists to
		// survive — see DefaultPace.
		if !throttled(ex) || attempt >= retries {
			return last, nil
		}
		wait *= 2
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// waitTurn holds the pace between two calls, measured from the previous one
// rather than slept blindly: a call that took two seconds has already waited.
func (c *Client) waitTurn(ctx context.Context, pace time.Duration) {
	if pace <= 0 || c.lastCall.IsZero() {
		return
	}
	if gap := pace - time.Since(c.lastCall); gap > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(gap):
		}
	}
}

func (c *Client) once(ctx context.Context, call string) (trace.Exchange, error) {
	method, path, body := c.request(call)
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, strings.NewReader(string(body)))
	if err != nil {
		return trace.Exchange{}, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.sign(req, body); err != nil {
		return trace.Exchange{}, fmt.Errorf("sign %s: %w", call, err)
	}

	started := time.Now()
	resp, err := c.http.Do(req)
	c.lastCall = time.Now()
	if err != nil {
		return trace.Exchange{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return trace.Exchange{}, err
	}

	ex := trace.Exchange{
		At:        started.UTC(),
		Method:    method,
		Path:      path,
		Operation: c.operationName(call),
		Provider:  string(c.Provider),
		Status:    resp.StatusCode,
		Ms:        float64(time.Since(started).Microseconds()) / 1000,
		Mounted:   true,
	}
	// Only the response. The request is not recorded at all, which is what
	// makes it impossible for a transcript from this package to carry an
	// Authorization header — a stronger guarantee than redacting one.
	var decoded any
	if json.Unmarshal(raw, &decoded) == nil {
		ex.Res = &trace.Message{Body: decoded}
	} else if len(raw) > 0 {
		ex.Res = &trace.Message{Body: string(raw)}
	}
	return ex, nil
}

// request turns a call into a request, in the provider's own dialect.
func (c *Client) request(call string) (method, path string, body []byte) {
	if c.Provider == Outscale {
		return http.MethodPost, "/api/v1/" + call, []byte("{}")
	}
	return http.MethodGet, call, nil
}

// operationName is what the catalogue keys this call under. Outscale actions
// carry the SDK's own prefix so a shape file and a route table agree; the other
// two are named by their path, which is what their operations are.
func (c *Client) operationName(call string) string {
	if c.Provider == Outscale {
		return "osc/Client." + call
	}
	return ""
}

// throttled reports whether an answer is a rate limit rather than a verdict.
//
// 429 is the honest form. Outscale's is a 400 carrying InvalidParameterValue
// 4120 — the same code it uses for a genuinely bad parameter, which is why this
// is a heuristic and why the retry is bounded: a real parameter error would
// otherwise be tried four times before being reported unchanged.
func throttled(ex trace.Exchange) bool {
	if ex.Status == http.StatusTooManyRequests {
		return true
	}
	if ex.Status != http.StatusBadRequest || ex.Res == nil {
		return false
	}
	obj, ok := ex.Res.Body.(map[string]any)
	if !ok {
		return false
	}
	errs, ok := obj["Errors"].([]any)
	if !ok || len(errs) == 0 {
		return false
	}
	first, ok := errs[0].(map[string]any)
	if !ok {
		return false
	}
	return first["Code"] == "4120"
}

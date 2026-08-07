package emulator

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
)

// The page the binary serves about itself.
//
// It shows what no other tool here shows: served, driven and probed side by
// side and never added up, and the versioned gap with the upstream surface. It
// deliberately lists no resource. `scw instance server list` does that better
// and with the real cloud's shape; a page that listed them would become a
// second source of truth and would invite a delete button.
//
// Three properties keep it honest, and each is mechanical rather than written
// down:
//
//   - It is mounted only when the listen address keeps the emulator on this
//     machine. Off loopback the page is not hidden, it does not exist.
//     TestThePageIsNotServedOffLoopback fails without that.
//   - It knows no provider. Every name it shows — provider, product, operation,
//     path — arrives as data from /_feint/routes, /_feint/health and the
//     coverage artefacts, so a fourth pack appears without a line of the asset
//     changing. TestTheEmbeddedPageNamesNoProvider greps the asset for the
//     three names and fails on any of them.
//   - Nothing it adds accepts a command: every route below is a GET.
//     TestThePageAddsOnlyGETRoutes enumerates the mux and fails otherwise.
//
// And no authentication, ever. The threat is a page in the operator's browser
// driving the emulator, and a secret held by that same browser is inherited by
// the hostile page — that is what CSRF is. What actually works is the Origin
// refusal in rebinding.go, which is measured. A login screen in front of a
// service that accepts every credential by design would be a fourth fake
// credential, the only one not documented as fake, and it would invite an
// operator to expose the port with confidence.
//
// There is no text/template here either. CLAUDE.md records that cloudinit is
// the only place in this repository where a structured format goes through a
// text template, and asks for no second one; the lesson cost a cloud-config
// injection through a multi-line SSH key. So the page is a static file served
// as it is, and every value reaches it through fetch and textContent.

//go:embed ui/index.html ui/app.css ui/app.js
var uiAssets embed.FS

var (
	uiPage   = mustAsset("ui/index.html")
	uiStyle  = mustAsset("ui/app.css")
	uiScript = mustAsset("ui/app.js")
)

// mustAsset reads an embedded file at start-up. A failure here is a typo in the
// name above, not a runtime condition: //go:embed already refuses to compile
// when a pattern matches nothing.
func mustAsset(name string) []byte {
	body, err := uiAssets.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("feint: embedded asset %s: %v", name, err))
	}
	return body
}

// UIDigest identifies the page this binary serves.
//
// It is computed from the embedded bytes, not from the files on disk, so it
// answers "what does this binary put on the screen" rather than "what is in the
// working tree" — the two differ for an installed binary, and the first is the
// one a screenshot depicts.
//
// It exists because the screenshots in docs/ cannot be checked by comparing
// pixels: the page renders wall-clock values by design, so two captures a second
// apart differ, and a gate demanding byte equality would be red forever and then
// disarmed. What can be checked is whether the page changed since the pictures
// of it were taken, and this is the number that says so.
//
// The name and the length of each asset go into the stream before its bytes, so
// two files whose contents were swapped are not the same page.
// TestEveryAssetMovesTheDigest fails when an asset stops being covered.
func UIDigest() string {
	sum := sha256.New()
	for _, asset := range []struct {
		name string
		body []byte
	}{
		{"index.html", uiPage},
		{"app.css", uiStyle},
		{"app.js", uiScript},
	} {
		fmt.Fprintf(sum, "%s %d\n", asset.name, len(asset.body))
		sum.Write(asset.body)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// UI is what the page needs from the process that owns the listen address.
//
// The address is here rather than on Env because it is not a pack's business:
// Env is what a provider pack needs from the core, and no pack has an opinion
// about where the process listens.
type UI struct {
	// Addr is the address the process is about to listen on. It decides whether
	// the page exists at all.
	Addr string
	// Version is what the header shows. Empty prints "dev", the same answer the
	// version command gives for a binary built from a checkout.
	Version string
	// Upstream reports the versioned gap between what the packs serve and what
	// the provider SDKs declare. A function rather than a value, so a
	// `mise run drift:update` during a session shows up on the next refresh
	// instead of at the next restart.
	//
	// It is supplied by the caller because reading coverage/*.json means
	// knowing that artefact's shape, and that shape names providers. The core
	// stays unable to name one (rule 5), and internal/cli — which already owns
	// the loader the docs command uses — hands the numbers over as data.
	Upstream func() UpstreamView
}

// UpstreamView is the gap the page displays, and the provenance without which
// the numbers would read as a live measurement rather than a versioned artefact.
type UpstreamView struct {
	// Available is false when no coverage artefact was found. The page then says
	// so and prints Refresh, rather than showing zeroes that look like a
	// perfectly covered API.
	Available bool `json:"available"`
	// Source is the directory the artefacts were read from.
	Source string `json:"source,omitempty"`
	// WrittenAt is when those files were last written, RFC 3339.
	//
	// It is the file's timestamp and not a scan date recorded inside it, and
	// that is a deliberate limit rather than an oversight: the weekly workflow
	// decides whether the upstream surface moved with `git diff --quiet --
	// coverage/`, so a timestamp stored in the artefact would differ on every
	// run and open a drift pull request every Monday whether or not anything
	// moved. The page says which of the two it is showing.
	WrittenAt string `json:"written_at,omitempty"`
	// Refresh is the command that recomputes the artefacts.
	Refresh string `json:"refresh"`
	// Products is one row per upstream product, provider included, so the page
	// can group them without knowing a single provider name.
	Products []UpstreamProduct `json:"products"`
	// Operations is the verdict on every upstream operation, with the reason a
	// declined one is declined.
	//
	// This is what turns each count above into the decisions it is made of. One
	// product declines 111 operations, and that number is not something a reader
	// can act on; the sentence written beside each refusal is. The sentences
	// already existed — Declined() has carried them since the packs were
	// written, and TestUnexplainedDeclinesAreFound keeps them non-empty — but
	// reading one meant running a scan against an SDK checkout, so nobody did.
	//
	// The join is never recomputed here. drift.Compare owns it, writes it into
	// the versioned artefact, and this carries it across unchanged: a second
	// implementation, in a browser, would be a second answer to the question the
	// whole project measures itself on.
	Operations []UpstreamOperation `json:"operations"`
}

// UpstreamProduct is one product of one provider's API surface.
type UpstreamProduct struct {
	Provider  string `json:"provider"`
	Product   string `json:"product"`
	Served    int    `json:"served"`
	Declined  int    `json:"declined"`
	Untriaged int    `json:"untriaged"`
	Total     int    `json:"total"`
}

// UpstreamOperation is one operation of one upstream API, and what this
// emulator decided about it.
type UpstreamOperation struct {
	Operation string `json:"operation"`
	Provider  string `json:"provider"`
	Product   string `json:"product"`
	Version   string `json:"version,omitempty"`
	// Status is the drift report's own word — implemented, declined, unknown —
	// rather than a second vocabulary invented at the boundary.
	Status string `json:"status"`
	// Reason is why a declined operation is declined, empty otherwise.
	Reason string `json:"reason,omitempty"`
}

// MountUI mounts the page and reports whether it did.
//
// The decision is separated from the act on purpose. A test that drove `serve`
// to check a listen-address refusal never returned, because with the refusal
// removed serve did its job and listened; the lesson, recorded in
// checkListenAddr in internal/cli, is that a predicate returns in microseconds
// and cannot hang in either direction. So this answers a question — is this
// address loopback — and the caller decides what to print.
//
// It must be called once, before serving. A second call panics inside net/http
// on the duplicate pattern, which is the same failure mode two packs claiming
// one route get, and NewServer already refuses that case for them.
func (s *Server) MountUI(ui UI) bool {
	if !LoopbackListen(ui.Addr) {
		// Not mounted, and one endpoint left to say why. A bare 404 from the
		// catch-all would leave an operator looking for a typo in the URL, and
		// the reason — the address they chose — is not something they can guess
		// from "404 page not found".
		s.mountSelf("GET /_feint/ui", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error": "the page is not served here: this emulator listens on " + ui.Addr +
					", which is not a loopback address. Off loopback the browser guard cannot tell " +
					"a local page from a hostile one, so the page is not mounted at all. " +
					"Start with --addr 127.0.0.1:4599 to get it.",
			})
		})
		return false
	}

	s.mountSelf("GET /_feint/ui", asset("text/html; charset=utf-8", uiPage))
	s.mountSelf("GET /_feint/ui/app.css", asset("text/css; charset=utf-8", uiStyle))
	s.mountSelf("GET /_feint/ui/app.js", asset("text/javascript; charset=utf-8", uiScript))
	// The inventory rides with the page rather than with the general endpoints,
	// because it is the one that publishes Runtime — see handleResources. Off
	// loopback it is not mounted at all, like everything else here.
	s.mountSelf("GET /_feint/resources", s.handleResources)
	s.mountSelf("GET /_feint/events", s.handleEvents)
	s.mountSelf("GET /_feint/ui/data", func(w http.ResponseWriter, _ *http.Request) {
		version := ui.Version
		if version == "" {
			version = "dev"
		}
		upstream := UpstreamView{}
		if ui.Upstream != nil {
			upstream = ui.Upstream()
		}
		if upstream.Refresh == "" {
			upstream.Refresh = upstreamRefreshCommand
		}
		if upstream.Products == nil {
			upstream.Products = []UpstreamProduct{}
		}
		if upstream.Operations == nil {
			upstream.Operations = []UpstreamOperation{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"version":  version,
			"upstream": upstream,
		})
	})
	return true
}

// upstreamRefreshCommand is what a reader has to run to move the numbers. It is
// shown next to them because a versioned figure without the way to refresh it is
// a figure people stop trusting rather than one they update.
const upstreamRefreshCommand = "mise run drift:update"

// asset serves one embedded file.
//
// The Content-Security-Policy is worth the four extra headers here even though
// the page loads nothing remote: it is the mechanical form of "this page never
// reaches the network", so a future edit that pastes in a CDN tag fails in the
// browser instead of shipping. 'self' is enough for the style and the script
// because neither is inline — which is also what keeps the policy free of
// unsafe-inline.
func asset(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; "+
				"img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		// No caching: the asset ships inside the binary, so a stale copy in a
		// browser outlives the build that fixed it.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

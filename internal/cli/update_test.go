package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsNewerComparesVersionsRatherThanStrings(t *testing.T) {
	cases := []struct {
		current, latest string
		newer, ok       bool
	}{
		{"0.1.0", "v0.2.0", true, true},
		{"v0.1.0", "0.1.1", true, true},
		{"0.9.0", "0.10.0", true, true}, // string comparison would say no
		{"0.2.0", "v0.2.0", false, true},
		{"0.3.0", "v0.2.9", false, true},
		// A build that is not a release compares to nothing, and saying "you are
		// out of date" to somebody running their own build would be a guess.
		{"dev", "v0.2.0", false, false},
		{"v0.0.0-20260730-abcdef", "v0.2.0", false, false},
		{"0.1.0", "nightly", false, false},
	}
	for _, c := range cases {
		newer, ok := isNewer(c.current, c.latest)
		if newer != c.newer || ok != c.ok {
			t.Errorf("isNewer(%q, %q) = (%v, %v), want (%v, %v)",
				c.current, c.latest, newer, ok, c.newer, c.ok)
		}
	}
}

// The message is what a user acts on, so its three shapes are asserted rather
// than assumed.
func TestUpdateMessageSaysWhatToDo(t *testing.T) {
	latest := githubRelease{TagName: "v0.2.0", HTMLURL: "https://example.invalid/releases/v0.2.0"}

	out := updateMessage("0.1.0", latest)
	for _, want := range []string{"a newer release is available: v0.2.0", "releases/download/v0.2.0", "sha256sum -c"} {
		if !strings.Contains(out, want) {
			t.Errorf("the upgrade message does not carry %q:\n%s", want, out)
		}
	}
	// The command must name the version, never `latest`: a mutable reference
	// cannot be checked against the checksum on the line below it.
	if strings.Contains(out, "releases/latest") {
		t.Errorf("the upgrade command uses a mutable reference:\n%s", out)
	}

	if out := updateMessage("0.2.0", latest); !strings.Contains(out, "this is the latest release") {
		t.Errorf("an up-to-date build was not told so:\n%s", out)
	}
	if out := updateMessage("dev", latest); !strings.Contains(out, "not a released version") {
		t.Errorf("a source build was compared to a release anyway:\n%s", out)
	}
}

func TestLatestReleaseReadsTheTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "feint/") {
			t.Errorf("user agent is %q", got)
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","html_url":"https://example.invalid/x","body":"ignored"}`))
	}))
	defer srv.Close()

	got, err := latestRelease(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.TagName != "v1.2.3" {
		t.Fatalf("tag is %q", got.TagName)
	}
}

// Not knowing is not the same as being current. Every failure below must be an
// error the caller can report, never a silent "you are up to date".
func TestLatestReleaseRefusesRatherThanGuesses(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"no release yet", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }},
		{"rate limited", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }},
		{"not a release document", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }},
		{"a release with no tag", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"html_url":"x"}`)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.handler)
			defer srv.Close()
			if _, err := latestRelease(context.Background(), srv.Client(), srv.URL); err == nil {
				t.Fatal("want an error, got none")
			}
		})
	}
}

// The whole command, including the half that must never happen: no network call
// when the flag is absent.
func TestVersionCheckOnlyReachesTheNetworkWhenAsked(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","html_url":"x"}`))
	}))
	defer srv.Close()

	var out, errOut strings.Builder
	if rc := versionCheck(nil, &out, &errOut); rc != exitOK {
		t.Fatalf("plain version exited %d", rc)
	}
	if reached {
		t.Fatal("`feint version` reached the network without being asked")
	}
	if !strings.Contains(out.String(), Version) {
		t.Fatalf("version not printed: %q", out.String())
	}

	// And the opt-out, for anyone scripting this where it must not dial out.
	t.Setenv("FEINT_NO_UPDATE_CHECK", "1")
	out.Reset()
	if rc := versionCheck([]string{"--check"}, &out, &errOut); rc != exitOK {
		t.Fatalf("exited %d", rc)
	}
	if reached {
		t.Fatal("FEINT_NO_UPDATE_CHECK did not stop the check")
	}
	if !strings.Contains(out.String(), "disabled by FEINT_NO_UPDATE_CHECK") {
		t.Fatalf("the opt-out was silent: %q", out.String())
	}
}

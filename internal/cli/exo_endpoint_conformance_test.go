//go:build conformance

package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/conformancetest"
	"github.com/stephrobert/feint/internal/s3"
)

func TestExoDropsConfiguredS3EndpointPrefix(t *testing.T) {
	var guardHits atomic.Int64
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		guardHits.Add(1)
		http.Error(w, "never forwarded", http.StatusBadGateway)
	}))
	defer guard.Close()
	if err := conformancetest.ProbeProxyGuard(t.Context(), guard.URL); err != nil || guardHits.Load() != 1 {
		t.Fatal("proxy guard did not observe its positive CONNECT refusal:", err)
	}
	aws := conformancetest.AWS(t, guard.URL, "")
	type exchange struct{ method, path string }
	observed := make(chan exchange, 8)
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case observed <- exchange{r.Method, r.URL.EscapedPath()}:
		default:
			t.Error("too many requests for the bounded endpoint measurement")
		}
		if r.Method == http.MethodHead {
			w.Header().Set("X-Amz-Bucket-Region", "ch-gva-2")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "measurement only", http.StatusNotImplemented)
	}))
	defer probe.Close()
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range srv.Packs() {
		if p.Name() != "exoscale" {
			continue
		}
		pack, ok := p.(packEnv)
		if !ok {
			t.Fatal("pack environment missing")
		}
		vars := pack.Env(probe.URL).Vars
		vars["EXOSCALE_SOS_ENDPOINT"] = probe.URL + s3.DoorPath("exoscale", "ch-gva-2")
		vars["EXOSCALE_STORAGE_API_ENDPOINT"] = vars["EXOSCALE_SOS_ENDPOINT"]
		exo := conformancetest.EXO(t, aws, vars)
		file := filepath.Join(aws.Home, "input")
		if err := os.WriteFile(file, []byte("fake measurement"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exo.Path, "storage", "upload", file, "sos://feint-bucket/exo-byte-proof")
		cmd.Env = exo.Env
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "S3: PutObject") || !strings.Contains(string(out), "501") {
			t.Fatalf("measurement did not reach its explicit PUT refusal: %v %q", err, out)
		}
		want := []exchange{{"HEAD", "/feint-bucket"}, {"PUT", "/feint-bucket/exo-byte-proof"}}
		for _, expected := range want {
			select {
			case got := <-observed:
				if got != expected {
					t.Fatalf("pinned client's endpoint behavior changed: got=%+v want=%+v", got, expected)
				}
				t.Logf("measured method=%s path=%s; configured prefix absent", got.method, got.path)
			default:
				t.Fatal("missing measured request:", expected)
			}
		}
		if len(observed) != 0 || guardHits.Load() != 1 {
			t.Fatalf("unexpected requests: local=%d refused external delta=%d", len(observed), guardHits.Load()-1)
		}
		t.Log("intentional measurement-only PUT refusal; no object lifecycle is certified")
		return
	}
	t.Fatal("Exoscale pack absent")
}

package exoscale

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/s3"
)

func TestSOSDefaultDiscoveryMatchesAuthorizedCloudCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/evidence/exoscale-sos-empty-bucket.json")
	if err != nil {
		t.Fatal(err)
	}

	var corpus struct {
		Exchanges []struct {
			Method, Query, Body string
			Status              int
		}
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.Open(t.Context(), filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatal(err)
	}
	profile := (&Pack{}).ObjectStorage("ch-gva-2")
	server, err := s3.New(store, s3.Config{Region: profile.Region, MaxObjectBytes: 1024,
		MissingBucket: profile.MissingBucket, BucketConfiguration: profile.BucketConfiguration,
		AbsentConfigurations: profile.AbsentConfigurations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	observed := 0
	for _, exchange := range corpus.Exchanges {
		if exchange.Query != "cors=" && exchange.Query != "ownershipControls=" {
			continue
		}
		t.Run(exchange.Query, func(t *testing.T) {
			type cloudError struct{ Code, Message, BucketName string }
			var expected cloudError
			if err := xml.Unmarshal([]byte(exchange.Body), &expected); err != nil || expected.Code == "" {
				t.Fatal("unreadable real error:", err)
			}
			if expected.BucketName != "" {
				expected.BucketName = "bucket"
			}
			request := httptest.NewRequest("GET", "http://localhost/bucket?"+exchange.Query, nil)
			request.RemoteAddr = "127.0.0.1:1234"
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			var actual cloudError
			if err := xml.Unmarshal(response.Body.Bytes(), &actual); err != nil || actual != expected || response.Code != exchange.Status {
				t.Fatalf("cloud error diverged: status=%d got=%+v want=%+v decode=%v", response.Code, actual, expected, err)
			}
			request.Method = http.MethodPut
			refused := httptest.NewRecorder()
			server.ServeHTTP(refused, request)
			if refused.Code < 400 {
				t.Fatal("unimplemented setting write falsely succeeded")
			}
			request.URL.Path = "/missing"
			request.Method = http.MethodGet
			missing := httptest.NewRecorder()
			server.ServeHTTP(missing, request)
			if missing.Code == exchange.Status && missing.Body.String() == response.Body.String() {
				t.Fatal("absent bucket was described as an existing bucket with absent settings")
			}
		})
		observed++
	}
	if observed != 2 {
		t.Fatal("corpus did not prove both default errors")
	}
}

func TestSOSUsageRefusesDisabledStorageAndLegacyDates(t *testing.T) {
	env := emulator.DefaultEnv()
	pack := New(env)
	request := httptest.NewRequest("GET", "/v2/sos-buckets-usage", nil)
	response := httptest.NewRecorder()
	pack.listSOSBucketsUsage(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatal("disabled storage returned a success-shaped inventory")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.Open(t.Context(), filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	env.Objects = store
	response = httptest.NewRecorder()
	pack.listSOSBucketsUsage(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "{\"sos-buckets-usage\":[]}\n" {
		t.Fatal("actual empty enabled inventory was refused:", response.Body.String())
	}
	if err := store.CreateBucket(t.Context(), "exoscale/ch-gva-2/legacy"); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	pack.listSOSBucketsUsage(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatal("legacy date was invented")
	}
}

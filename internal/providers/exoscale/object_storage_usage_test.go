package exoscale

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/objectstore"
)

func TestSOSUsageReportsOnlyPublishedScopedBytesAndRefusesMalformedScopes(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.Open(t.Context(), filepath.Join(root, "bytes"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	env := emulator.DefaultEnv()
	env.Objects = store
	pack := New(env)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	metadata := map[string]string{"s3.created": created.Format(time.RFC3339Nano)}
	if err := store.CreateBucketWithMetadata(t.Context(), "exoscale/ch-gva-2/bucket", metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject(t.Context(), "exoscale/ch-gva-2/bucket", "key", bytes.NewReader([]byte("abc")), objectstore.PutOptions{Size: 3}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket(t.Context(), "scaleway/foreign"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/v2/sos-buckets-usage", nil)
	response := httptest.NewRecorder()
	pack.listSOSBucketsUsage(response, request)
	var actual struct {
		Items []sosBucketUsage `json:"sos-buckets-usage"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil || response.Code != 200 ||
		len(actual.Items) != 1 || actual.Items[0].Name != "bucket" || actual.Items[0].Zone != "ch-gva-2" ||
		actual.Items[0].Size != 3 || !actual.Items[0].Created.Equal(created) {
		t.Fatal("native usage invented data or included foreign bytes:", err, response.Code, response.Body.String())
	}
	for _, scope := range []string{"exoscale/no-zone/bucket", "exoscale/ch-gva-2/", "exoscale/ch-gva-2/bucket/nested"} {
		if err := store.CreateBucketWithMetadata(t.Context(), scope, metadata); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		pack.listSOSBucketsUsage(response, request)
		if response.Code != http.StatusInternalServerError {
			t.Fatal("malformed persisted scope was served:", scope, response.Body.String())
		}
		if err := store.DeleteBucket(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
	}
}

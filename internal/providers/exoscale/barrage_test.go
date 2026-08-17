package exoscale_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/core/store/storetest"
	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// The barrage, and the invariant sweep that follows it.
//
// Same reasoning as the two beside it, and the same sweep: it lives in the core,
// keyed on nothing but resource.Resource, so no pack defines its own idea of
// coherent. What is Exoscale's own is the traffic — a REST path per resource,
// operations that answer asynchronously, and elastic IPs allocated from a pool
// this pack keeps.

const exoscaleBarrageWorkers = 8

func newExoscaleBarrageServer(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, exoscale.New(env))
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	return srv.Handler(), env.Store
}

// callRaw is call() without the testing.T: a goroutine may not call t.Fatalf,
// and a helper that does turns a failure into undefined behaviour.
func callRaw(h http.Handler, method, path, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	decoded := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	}
	return rec.Code, decoded
}

func TestAnExoscaleBarrageLeavesTheStoreCoherent(t *testing.T) {
	h, st := newExoscaleBarrageServer(t)

	var wg sync.WaitGroup
	problems := make(chan string, exoscaleBarrageWorkers*8)

	for w := range exoscaleBarrageWorkers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			tag := fmt.Sprintf("w%d", worker)

			// An elastic IP per worker: the shared pool, which is where a
			// missing lock hands one address to two resources.
			if status, _ := callRaw(h, "POST", "/v2/elastic-ip", `{"description":"barrage-`+tag+`"}`); status != http.StatusOK {
				problems <- fmt.Sprintf("%s: elastic-ip answered %d", tag, status)
			}

			status, _ := callRaw(h, "POST", "/v2/security-group",
				`{"name":"barrage-`+tag+`","description":"barrage"}`)
			if status != http.StatusOK {
				problems <- fmt.Sprintf("%s: security-group answered %d", tag, status)
			}

			for i := range 2 {
				status, created := callRaw(h, "POST", "/v2/instance", fmt.Sprintf(`{
					"name": "barrage-%s-%d",
					"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
					"template": {"id": "11111111-1111-4111-8111-111111111111"},
					"disk-size": 10,
					"public-ip-assignment": "inet4"
				}`, tag, i))
				if status != http.StatusOK {
					problems <- fmt.Sprintf("%s-%d: instance create answered %d", tag, i, status)
					continue
				}
				// Exoscale answers an operation carrying the resource it acted
				// on, which is the shape the CLI follows to find the id.
				ref, _ := created["reference"].(map[string]any)
				id, _ := ref["id"].(string)
				if id == "" {
					problems <- fmt.Sprintf("%s-%d: the operation names no resource", tag, i)
					continue
				}

				// A block volume attached to that instance, so the orphan sweep
				// below has something to find. Without this the sweep runs over
				// a store where nothing names an instance and passes by finding
				// nothing, which reads exactly like a clean pack (#12).
				status, volume := callRaw(h, "POST", "/v2/block-storage", fmt.Sprintf(`{
					"name": "barrage-%s-%d", "size": 10
				}`, tag, i))
				if status != http.StatusOK {
					problems <- fmt.Sprintf("%s-%d: block volume create answered %d", tag, i, status)
				} else {
					ref, _ := volume["reference"].(map[string]any)
					volumeID, _ := ref["id"].(string)
					if volumeID == "" {
						problems <- fmt.Sprintf("%s-%d: the volume operation names no resource", tag, i)
					} else if status, _ := callRaw(h, "PUT", "/v2/block-storage/"+volumeID+":attach",
						`{"instance": {"id": "`+id+`"}}`); status != http.StatusOK {
						problems <- fmt.Sprintf("%s-%d: attach answered %d", tag, i, status)
					} else {
						// Detached before the instance goes: an attached volume
						// refuses its own delete, and the order a client walks
						// is the order this barrage walks.
						callRaw(h, "PUT", "/v2/block-storage/"+volumeID+":detach", "")
						callRaw(h, "DELETE", "/v2/block-storage/"+volumeID, "")
					}
				}

				callRaw(h, "DELETE", "/v2/instance/"+id, "")
			}
		}(w)
	}
	wg.Wait()
	close(problems)

	var refused []string
	for p := range problems {
		refused = append(refused, p)
	}
	if len(refused) > 0 {
		t.Errorf("%d request(s) the barrage did not expect to fail:\n%s",
			len(refused), strings.Join(refused, "\n"))
	}

	// That day came with EXO-4 (#12). This block used to explain why
	// storetest.Orphans was not called — every reference in this pack was held
	// by the owner, so deleting the owner took it along — and it named the
	// condition for its own expiry: *the day a resource here names an instance,
	// it declares Owns and joins the sweep the two other packs run (#215)*. A
	// block volume names the instance it is attached to, so the pack declares
	// Owns and the sweep runs here like everywhere else.
	if found := storetest.Orphans(st.All(), exoscale.Owns, nil); len(found) != 0 {
		t.Errorf("the barrage left resources naming an owner that is gone:\n%s",
			strings.Join(found, "\n"))
	}

	if found := storetest.Sweep(st.All(), nil, nil); len(found) != 0 {
		t.Errorf("the store is incoherent after the barrage:\n%s", strings.Join(found, "\n"))
	}
}

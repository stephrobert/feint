package emulator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// MaxBody caps request bodies. The emulator only ever receives control-plane
// payloads; anything larger is a client bug or an attempt to exhaust memory.
//
// Exported because a pack that reads a body before the decoder does — the
// Outscale DryRun probe — has to use the same number. It used its own, one
// quarter of this one, and put the truncated body back for the handler: a valid
// 1.2 MiB request came out as "unexpected end of JSON input". Two constants for
// one limit is how they disagree.
const MaxBody = 4 << 20

// writeJSON serializes v with the given status. Encoding errors are not
// reported to the client: the status line is already written by then, so the
// only honest thing left is to stop.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteJSON is the packs' entry point for successful responses.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// DecodeJSON reads a JSON request body into v. A missing body decodes to the
// zero value rather than an error: several provider APIs accept empty payloads
// on create, and their SDKs rely on it.
//
// It also reports, to whoever is observing the request, every field the client
// sent that v does not declare. That is the half of the invented-field problem
// the response check cannot see. Validating what we answer catches a field we
// made up; nothing caught a field the client sent and we silently dropped —
// which is exactly how CreateVms shipped reading a VmCount where Outscale sends
// MinVmsCount and MaxVmsCount, so every request created one machine whatever it
// asked for. json.Unmarshal is happy to ignore it, and did.
//
// The signature is unchanged on purpose: fifty call sites would otherwise have
// to thread a value none of them care about. The report travels on the request
// context, put there by the observer.
func DecodeJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return err
	}
	if rep, watching := r.Context().Value(reportKey{}).(*requestReport); watching {
		rep.add(unreadFields(body, v))
	}
	return nil
}

// MarkRead records that something outside the handler's own request type read a
// field, so the report stops calling it unread.
//
// It exists for one shape: a value consumed before the handler runs. Outscale
// declares DryRun on all twenty of its actions and answers it at the mount
// point, so no handler decodes it — which made `DryRun: false` count as a field
// no handler read, and a perfectly legitimate request fail this project's own
// conformance gate. Declaring the field on twenty request structs instead would
// have quietened the report by lying to it: the handlers still would not read
// it.
//
// It is deliberately narrow. A pack that calls this for a field it simply
// ignores has disabled the check that exists to catch exactly that, so the call
// belongs next to the code that does the reading, and nowhere else.
func MarkRead(r *http.Request, fields ...string) {
	rep, watching := r.Context().Value(reportKey{}).(*requestReport)
	if !watching {
		return
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	if rep.read == nil {
		rep.read = make(map[string]bool, len(fields))
	}
	for _, field := range fields {
		rep.read[field] = true
	}
}

// requestReport collects what one request turned out to carry and nobody read.
type requestReport struct {
	mu     sync.Mutex
	unread []string
	read   map[string]bool
}

// reportKey is the private context key the observer files a report under.
type reportKey struct{}

// contextWithReport attaches a report to a request's context.
func contextWithReport(r *http.Request, rep *requestReport) context.Context {
	return context.WithValue(r.Context(), reportKey{}, rep)
}

func (rep *requestReport) add(fields []string) {
	if len(fields) == 0 {
		return
	}
	rep.mu.Lock()
	rep.unread = append(rep.unread, fields...)
	rep.mu.Unlock()
}

func (rep *requestReport) fields() []string {
	rep.mu.Lock()
	defer rep.mu.Unlock()
	out := make([]string, 0, len(rep.unread))
	for _, field := range rep.unread {
		if !rep.read[field] {
			out = append(out, field)
		}
	}
	return out
}

// unreadFields lists the JSON keys the body carries that v's type does not
// declare, nested paths included.
//
// Nested matters as much as the top level: an Outscale request puts most of its
// arguments under Filters, so a misspelt filter is a request that silently
// matches everything.
//
// It costs a second decode of the body into a map. That is real, and it is why
// this is worth it anyway: these are control-plane payloads of a few hundred
// bytes, against an emulator whose next action may be to boot a container.
func unreadFields(body []byte, v any) []string {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		// Not an object: an array or a scalar body has no field names to check.
		return nil
	}
	var out []string
	collectUnread("", raw, reflect.TypeOf(v), &out)
	sort.Strings(out)
	return out
}

func collectUnread(prefix string, raw map[string]any, t reflect.Type, out *[]string) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// Anything but a struct accepts every key by construction — a map, or an
	// any — so there is nothing to report.
	if t == nil || t.Kind() != reflect.Struct {
		return
	}
	fields := jsonFields(t)

	for name, value := range raw {
		// Looked up folded, because encoding/json falls back to a
		// case-insensitive match when no field matches exactly. Reporting a key
		// it did read would be a false positive, and a report with those in it
		// stops being read.
		field, declared := fields[strings.ToLower(name)]
		if !declared {
			*out = append(*out, prefix+name)
			continue
		}
		switch nested := value.(type) {
		case map[string]any:
			collectUnread(prefix+name+".", nested, field.Type, out)
		case []any:
			elem := field.Type
			for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Slice {
				elem = elem.Elem()
			}
			for i, item := range nested {
				if obj, ok := item.(map[string]any); ok {
					collectUnread(prefix+name+"["+strconv.Itoa(i)+"].", obj, elem, out)
				}
			}
		}
	}
}

// fieldCache keeps the json name to field mapping per type, folded to lower
// case. Reflection over a struct is not free and these types are few and fixed.
var fieldCache sync.Map // reflect.Type -> map[string]reflect.StructField

func jsonFields(t reflect.Type) map[string]reflect.StructField {
	// Dereferenced first. A struct embedded by pointer — type target struct{
	// *Base } — made NumField panic on a non-struct, and this runs on every
	// observed request of all three packs, so the first type factored that way
	// would have taken every one of them down.
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	if cached, ok := fieldCache.Load(t); ok {
		return cached.(map[string]reflect.StructField)
	}

	fields := make(map[string]reflect.StructField, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported: encoding/json ignores it, so must this
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		tagged := name != ""
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		// An embedded struct with no tag is inlined by encoding/json: its own
		// type name is not a key, its fields are. Registering the name too was a
		// false negative — a client sending {"Base": ...} would have been told
		// nothing, where encoding/json ignores it.
		if f.Anonymous && !tagged {
			for inner, innerField := range jsonFields(f.Type) {
				if _, taken := fields[inner]; !taken {
					fields[inner] = innerField
				}
			}
			continue
		}
		fields[strings.ToLower(name)] = f
	}

	fieldCache.Store(t, fields)
	return fields
}

// EndpointOf rebuilds the address the caller reached this server on.
//
// Every provider has a route that hands a client the address to call next —
// Outscale's ReadRegions, Exoscale's list-zones — and every one of them must
// answer with this emulator rather than with the real cloud. Answering
// api.eu-west-2.outscale.com sends the client's next request out to a paying
// account, which is the one failure an emulator must never have.
//
// The Host header is what the client asked for, which is exactly the value it
// should be told to keep using: a client reaching the emulator through a
// container name, a tunnel or a port mapping gets its own view back, not the
// server's idea of where it lives.
func EndpointOf(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

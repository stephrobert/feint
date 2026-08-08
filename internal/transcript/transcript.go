// Package transcript turns a proxy recording into the three answers a developer
// needs before serving one more operation.
//
// `feint proxy` writes what a real client and a real cloud said to each other as
// JSON Lines of [trace.Exchange]. That file is the measurement this project's
// whole method rests on — "on ne suit pas l'API à la main, on la mesure" — but a
// measurement nobody can read is a file, not a tool. Reading it with jq means
// knowing that an unserved operation carries mounted:false and is named only by
// its path, that a served one carries its operation, and that the shape lives
// three keys deep in res.body. This package is that knowledge, executable, so the
// developer asks a question instead of composing a filter.
//
// It answers exactly the three questions CLAUDE.md says cost the most guessing:
//
//   - which operation to serve next — [Unserved], the operations a real client
//     called that no pack claims, ranked so the ones with a populated response
//     float up, because an empty list teaches nothing about a shape;
//   - what the response must look like — [Shape], the field tree of what the real
//     cloud actually returned, which is not what the SDK says it may return;
//   - what the emulator already gets wrong — [Diff], the fields the real cloud
//     returns for an operation that the emulator's own answer omits.
//
// It owns no shape of its own: every exchange is a [trace.Exchange], the one the
// proxy wrote and the emulator's ring publishes, imported rather than
// redeclared. A second declaration would drift, and this repository has paid for
// that twice.
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/trace"
)

// Load reads a JSON Lines transcript.
//
// A blank line is skipped rather than failed on: an editor that added a trailing
// newline must not make a recording unreadable. A malformed line is an error
// with its number, because a transcript with one bad line is a bug worth seeing,
// not one worth silently dropping a measurement over.
func Load(r io.Reader) ([]trace.Exchange, error) {
	var out []trace.Exchange
	sc := bufio.NewScanner(r)
	// A single control-plane response can be tens of kilobytes, and a body is
	// recorded inline; the default 64 KiB token is too small for a populated
	// ReadSecurityGroups. One megabyte matches the proxy's own default body cap.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var x trace.Exchange
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		// UseNumber for the same reason the proxy recorded with it: a 19-digit
		// identifier read through float64 comes back changed, and a shape report
		// that says "number" must not have altered the value it read to say so.
		dec.UseNumber()
		if err := dec.Decode(&x); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, x)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Op is one operation a client addressed, and how much it exercised it.
type Op struct {
	// Path is the request path, which names an unserved Outscale operation when
	// nothing else does: a call no pack claims carries an empty Operation, so the
	// path is the only handle on what to implement.
	Path string `json:"path"`
	// Operation is the SDK name when a pack claimed the route, empty otherwise.
	Operation string `json:"operation,omitempty"`
	// Calls is how many times it was seen. The ranking key: an operation a real
	// client walked ten times outranks one it touched once.
	Calls int `json:"calls"`
	// MaxBytes is the largest response body observed, the signal that separates
	// an operation with a shape to copy from one that answered an empty list.
	// A developer implementing from this transcript wants the populated ones.
	MaxBytes int `json:"max_response_bytes"`
	// Statuses are the distinct HTTP statuses seen, sorted. A 400 among them says
	// the call needs a parameter the empty probe did not send — the operation
	// exists, its shape is just not in this recording.
	Statuses []int `json:"statuses"`
}

// Display names the operation the way a developer refers to it: the SDK
// operation when a pack claimed the route, and the request path otherwise, which
// for an unserved Outscale call is the only name the recording carries.
func (o Op) Display() string {
	if o.Operation != "" {
		return o.Operation
	}
	return o.Path
}

// Unserved returns the operations a client called that no pack claims, ranked.
//
// This is the work queue #74 ranks, derived from a measurement instead of a
// guess. The ranking is calls first, then the size of the largest response:
// among operations called equally often, the one that returned a populated body
// is the one a developer can implement from, so it comes first.
func Unserved(exs []trace.Exchange) []Op {
	return aggregate(exs, false)
}

// Served returns the operations a client called that a pack does claim, ranked
// the same way. It is what [Diff] has something to compare, and on its own it is
// the proof the recording exercised the pack at all.
func Served(exs []trace.Exchange) []Op {
	return aggregate(exs, true)
}

func aggregate(exs []trace.Exchange, mounted bool) []Op {
	byPath := map[string]*Op{}
	seenStatus := map[string]map[int]bool{}
	for i := range exs {
		x := &exs[i]
		if x.Mounted != mounted {
			continue
		}
		op := byPath[x.Path]
		if op == nil {
			op = &Op{Path: x.Path, Operation: x.Operation}
			byPath[x.Path] = op
			seenStatus[x.Path] = map[int]bool{}
		}
		op.Calls++
		if n := bodyBytes(x.Res); n > op.MaxBytes {
			op.MaxBytes = n
		}
		if !seenStatus[x.Path][x.Status] {
			seenStatus[x.Path][x.Status] = true
			op.Statuses = append(op.Statuses, x.Status)
		}
	}
	out := make([]Op, 0, len(byPath))
	for _, op := range byPath {
		sort.Ints(op.Statuses)
		out = append(out, *op)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		if out[i].MaxBytes != out[j].MaxBytes {
			return out[i].MaxBytes > out[j].MaxBytes
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// bodyBytes is a size proxy: the marshalled length of the recorded body, which
// is what makes "populated" comparable across operations without keeping the
// bodies. A nil body is zero, which is exactly the empty-list case we deprioritise.
func bodyBytes(m *trace.Message) int {
	if m == nil || m.Body == nil {
		return 0
	}
	b, err := json.Marshal(m.Body)
	if err != nil {
		return 0
	}
	return len(b)
}

// Field is one leaf or node of a response shape: a dotted path and the JSON type
// found there.
type Field struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

// Shape is the field tree of what an operation actually returned.
//
// selector matches either the full path (/api/v1/ReadNics) or the bare action
// (ReadNics or the operation name), so a developer names the operation the way
// they think of it. The shape is merged across every response for that
// operation: a field one element carries and another omits appears, because a
// developer implementing the type wants the union, not whichever element came
// first.
//
// The second return is false when the transcript has no such operation, which a
// caller reports rather than printing an empty shape that reads like "returns
// nothing".
func Shape(exs []trace.Exchange, selector string) ([]Field, bool) {
	fields := map[string]string{}
	found := false
	for i := range exs {
		x := &exs[i]
		if !matches(x, selector) || x.Res == nil {
			continue
		}
		found = true
		walk("", x.Res.Body, fields)
	}
	if !found {
		return nil, false
	}
	return sortedFields(fields), true
}

// FieldDiff is one field where a real cloud response and the emulator's differ.
type FieldDiff struct {
	Path string `json:"path"`
	// Real and Emu are the JSON types on each side; an empty Emu means the
	// emulator omitted the field entirely, which is the defect this exists to
	// surface: a field the real cloud returns and the emulator does not.
	Real string `json:"real"`
	Emu  string `json:"emu,omitempty"`
}

// Diff compares one operation's real-cloud shape to the emulator's.
//
// real is a transcript recorded against the cloud, emu one recorded against the
// emulator, for the same operation. What comes back is every field the real
// cloud returned that the emulator got wrong: absent (Emu empty) or a different
// type. It does not report fields the emulator adds and the cloud omits — a
// contract already refuses those, and this is the half no contract can see,
// because the contract is built from the SDK and the SDK is what the emulator
// was already written against.
func Diff(real, emu []trace.Exchange, selector string) ([]FieldDiff, bool) {
	realShape, okR := Shape(real, selector)
	if !okR {
		return nil, false
	}
	emuShape, _ := Shape(emu, selector)
	emuType := map[string]string{}
	for _, f := range emuShape {
		emuType[f.Path] = f.Type
	}
	var out []FieldDiff
	for _, f := range realShape {
		et, ok := emuType[f.Path]
		switch {
		case !ok:
			out = append(out, FieldDiff{Path: f.Path, Real: f.Type, Emu: ""})
		case et != f.Type:
			out = append(out, FieldDiff{Path: f.Path, Real: f.Type, Emu: et})
		}
	}
	return out, true
}

// matches reports whether an exchange addresses the operation named by selector.
func matches(x *trace.Exchange, selector string) bool {
	if selector == "" {
		return false
	}
	if x.Path == selector || x.Operation == selector {
		return true
	}
	// The bare action, which is the last path segment for Outscale and the
	// suffix of the operation name for every provider. A developer types
	// "ReadNics", not "/api/v1/ReadNics".
	if lastSegment(x.Path) == selector {
		return true
	}
	if x.Operation != "" && lastSegment(strings.ReplaceAll(x.Operation, ".", "/")) == selector {
		return true
	}
	return false
}

func lastSegment(s string) string {
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// walk records the type at each path of a decoded JSON value.
//
// The container and its element are distinct paths: a field holding a list is
// "array" at its own path, and its element shape lives under "path[]". Keeping
// them apart is what stops an empty list and a populated one from reading as a
// type change — "Volumes" is an array in both, and only "Volumes[]" and below
// differ when one recording had elements and the other did not. Every element is
// walked and its fields merged, so a field only some elements carry still
// appears.
func walk(path string, v any, into map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		if path != "" {
			into[path] = "object"
		}
		for k, nested := range val {
			child := k
			if path != "" {
				child = path + "." + k
			}
			walk(child, nested, into)
		}
	case []any:
		if path != "" {
			into[path] = "array"
		}
		for _, item := range val {
			walk(path+"[]", item, into)
		}
	default:
		into[path] = jsonType(v)
	}
}

// jsonType names the JSON type of a scalar as a transcript carries it. A number
// read with UseNumber is json.Number; nil is "null", which is a real and
// distinct answer from a field being absent.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case float64:
		return "number"
	case string:
		return "string"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func sortedFields(fields map[string]string) []Field {
	out := make([]Field, 0, len(fields))
	for p, t := range fields {
		out = append(out, Field{Path: p, Type: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

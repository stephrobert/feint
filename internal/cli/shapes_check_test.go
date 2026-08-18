package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/scaleway"
	"github.com/stephrobert/feint/internal/shape"
	"github.com/stephrobert/feint/internal/transcript"
)

// An element shape the emulator did not produce is not a missing field.
//
// A list that came back empty carries no element, so `Vms[]` and everything
// under it is absent — because the store is empty, not because the view forgot
// anything. Reported, it flagged nine operations on the first real run and
// drowned the findings that mattered. A gate that cries wolf on an empty store
// is a gate somebody turns off.
func TestAnEmptyListIsNotAMissingField(t *testing.T) {
	want := []transcript.Field{
		{Path: "Vms", Type: "array"},
		{Path: "Vms[]", Type: "object"},
		{Path: "Vms[].VmId", Type: "string"},
		{Path: "Vms[].Placement", Type: "object"},
	}
	// The emulator answered {"Vms": []}: the container is there, no element is.
	got := map[string]string{"Vms": "array"}

	if gaps := absentFrom(want, got); len(gaps) != 0 {
		t.Errorf("an empty list reported %d missing field(s): %v", len(gaps), gaps)
	}

	// And the accepting half, without which this rule would hide every real
	// omission: with an element present, a field it lacks is reported.
	got["Vms[]"] = "object"
	got["Vms[].VmId"] = "string"
	gaps := absentFrom(want, got)
	if len(gaps) != 1 || gaps[0].Path != "Vms[].Placement" {
		t.Errorf("a genuinely missing field was not reported: %v", gaps)
	}
}

// A dictionary's absent keys are data, not fields.
//
// `/products/servers` answers a map keyed by product name: about ninety entries
// on the real cloud, four in the emulated catalogue, deliberately —
// docs/limits.md calls the catalogue fiction. Counted as fields, those keys
// produced 46 of 113 findings on the first run.
//
// A dictionary is recognised by its values sharing one sub-shape, which is what
// separates it from a schema whose fields differ from one another.
func TestADictionarysKeysAreNotMissingFields(t *testing.T) {
	// Four products, each with the same three fields: a dictionary.
	var want []transcript.Field
	want = append(want, transcript.Field{Path: "servers", Type: "object"})
	for _, name := range []string{"DEV1-S", "DEV1-M", "GP1-XS", "PRO2-XXS"} {
		want = append(want,
			transcript.Field{Path: "servers." + name, Type: "object"},
			transcript.Field{Path: "servers." + name + ".ncpus", Type: "number"},
			transcript.Field{Path: "servers." + name + ".ram", Type: "number"},
			transcript.Field{Path: "servers." + name + ".arch", Type: "string"},
		)
	}
	// The emulator publishes one of the four, fully.
	got := map[string]string{
		"servers": "object", "servers.DEV1-S": "object",
		"servers.DEV1-S.ncpus": "number", "servers.DEV1-S.ram": "number",
		"servers.DEV1-S.arch": "string",
	}
	if gaps := absentFrom(want, got); len(gaps) != 0 {
		t.Errorf("dictionary keys were reported as missing fields: %v", gaps)
	}

	// The accepting half, and it is the one that matters: a field missing from
	// a key the emulator does publish is still reported. Without it this rule
	// would hide exactly the class of defect the gate exists for — #93 is that
	// case, eleven fields absent from products the emulator has.
	delete(got, "servers.DEV1-S.arch")
	gaps := absentFrom(want, got)
	if len(gaps) != 1 || gaps[0].Path != "servers.DEV1-S.arch" {
		t.Errorf("a field missing from a published key was not reported: %v", gaps)
	}
}

// The gate must cover whatever the server mounts, not a list written beside it.
//
// The provider names used to be hardcoded in checkShapes, which made a fourth
// pack invisible to the gate rather than reported: the honest "no catalogue;
// nothing to check" degradation existed in the code and was unreachable for a
// pack the list omitted. packsFor is the same seam
// TestCoverageRefusesAPackWithUnusableRefusals uses, and for the same reason —
// proving the call site rather than a helper.
func TestShapesCheckCoversEveryMountedPack(t *testing.T) {
	original := packsFor
	t.Cleanup(func() { packsFor = original })
	packsFor = func(env *emulator.Env) ([]emulator.Pack, error) {
		packs, err := original(env)
		if err != nil {
			return nil, err
		}
		return append(packs, stubPack{name: "fourthcloud"}), nil
	}

	var out, errOut strings.Builder
	if rc := checkShapes(t.TempDir(), nil, &out, &errOut); rc != exitOK {
		t.Fatalf("checkShapes exited %d with no catalogue recorded, want %d\n%s",
			rc, exitOK, errOut.String())
	}
	for _, name := range []string{"scaleway", "outscale", "exoscale", "fourthcloud"} {
		if !strings.Contains(out.String(), name+": no catalogue") {
			t.Errorf("the gate never accounted for the mounted pack %q:\n%s", name, out.String())
		}
	}
}

// stubPack is a mounted pack the shapes gate has no catalogue for. It serves
// nothing: what is being proven is that the gate accounts for it anyway.
type stubPack struct{ name string }

func (p stubPack) Name() string                    { return p.name }
func (p stubPack) Routes() []emulator.Route        { return nil }
func (p stubPack) Declined() []emulator.Decline    { return nil }
func (p stubPack) Env(string) emulator.Environment { return emulator.Environment{} }

// fieldDecliningPack grafts an injected field-decline list onto a real pack, so
// these tests do not depend on what the pack actually declines today: the
// mechanism is what is under test, the repository's own declines are covered by
// TestEveryDeclinedFieldSaysWhy and by the gate run in CI.
type fieldDecliningPack struct {
	emulator.Pack
	fields []emulator.FieldDecline
}

func (p fieldDecliningPack) DeclinedFields() []emulator.FieldDecline { return p.fields }

// aReason passes the shared reason guard; what these tests exercise is the
// matching, not the guard, which has its own tests in internal/core/emulator.
const aReason = "a bound for volumes this emulator never attaches, stated here only to exercise the gate"

// declineScaleway mounts the real Scaleway pack alone, wearing the injected
// declines, and restores packsFor on cleanup.
func declineScaleway(t *testing.T, fields ...emulator.FieldDecline) {
	t.Helper()
	original := packsFor
	t.Cleanup(func() { packsFor = original })
	packsFor = func(env *emulator.Env) ([]emulator.Pack, error) {
		return []emulator.Pack{fieldDecliningPack{Pack: scaleway.New(env), fields: fields}}, nil
	}
}

// productsServersOp is the one real operation these tests compare: the Scaleway
// catalogue, whose per_volume_constraint the emulator serves empty on purpose.
const productsServersOp = "GET /instance/v1/zones/fr-par-1/products/servers"

// writeShapeFile commits a hand-built catalogue for the gate to read.
func writeShapeFile(t *testing.T, dir string, fields ...transcript.Field) {
	t.Helper()
	writeShapeFileFor(t, dir, productsServersOp, fields...)
}

func writeShapeFileFor(t *testing.T, dir, key string, fields ...transcript.Field) {
	t.Helper()
	cat := shape.New("scaleway")
	cat.Operations[key] = &shape.Operation{
		Method:   "GET",
		Path:     strings.TrimPrefix(key, "GET "),
		Fields:   fields,
		Statuses: []int{200},
	}
	if err := writeCatalogue(filepath.Join(dir, "scaleway.json"), cat); err != nil {
		t.Fatal(err)
	}
}

// A gap the pack declines is excused, printed with its reason, and does not
// redden the gate; a gap it does not decline still does. This is the pair the
// mechanism exists for — without the decline the first half exits 2 (proven by
// the second half, same catalogue plus one undeclared field), and without the
// matching in matchingDecline the first half fails.
func TestADeclinedFieldIsExcusedAndAnUndeclaredOneIsNot(t *testing.T) {
	declineScaleway(t, emulator.FieldDecline{
		Operation: productsServersOp,
		Path:      "servers.*.per_volume_constraint.l_ssd",
		Reason:    aReason,
	})
	observed := []transcript.Field{
		{Path: "servers", Type: "object"},
		{Path: "servers.DEV1-S", Type: "object"},
		{Path: "servers.DEV1-S.per_volume_constraint", Type: "object"},
		{Path: "servers.DEV1-S.per_volume_constraint.l_ssd", Type: "object"},
		{Path: "servers.DEV1-S.per_volume_constraint.l_ssd.min_size", Type: "number"},
	}

	dir := t.TempDir()
	writeShapeFile(t, dir, observed...)
	var out, errOut strings.Builder
	if rc := checkShapes(dir, nil, &out, &errOut); rc != exitOK {
		t.Fatalf("a declined gap exited %d, want %d\n%s%s", rc, exitOK, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "declined servers.DEV1-S.per_volume_constraint.l_ssd: "+aReason) {
		t.Errorf("the excused field and its reason were not printed:\n%s", out.String())
	}

	// The refusing half: one more observed field, not declined, and the gate
	// answers the drift code again. Without it the decline mechanism could
	// excuse everything and this suite would stay green.
	dir2 := t.TempDir()
	writeShapeFile(t, dir2, append(observed,
		transcript.Field{Path: "servers.DEV1-S.invented_by_this_test", Type: "string"})...)
	out.Reset()
	errOut.Reset()
	if rc := checkShapes(dir2, nil, &out, &errOut); rc != exitDrift {
		t.Fatalf("an undeclared gap exited %d, want %d\n%s", rc, exitDrift, out.String())
	}
	if !strings.Contains(out.String(), "missing  servers.DEV1-S.invented_by_this_test") {
		t.Errorf("the undeclared gap was not named:\n%s", out.String())
	}
}

// A decline that excuses nothing is stale, and stale is a failure: the field is
// served now, or was never observed missing, and either way the entry describes
// a decision that no longer exists. Letting it rest would let the declined list
// rot into fiction — the exact analogue of an orphan Route.Operation.
func TestAStaleFieldDeclineFailsTheGate(t *testing.T) {
	declineScaleway(t, emulator.FieldDecline{
		Operation: productsServersOp,
		Path:      "servers.*.per_volume_constraint.l_ssd",
		Reason:    aReason,
	})
	// The catalogue observes only fields the emulator serves: no gap, so the
	// decline has nothing to excuse.
	dir := t.TempDir()
	writeShapeFile(t, dir,
		transcript.Field{Path: "servers", Type: "object"},
		transcript.Field{Path: "servers.DEV1-S", Type: "object"},
		transcript.Field{Path: "servers.DEV1-S.ncpus", Type: "number"},
	)

	var out, errOut strings.Builder
	if rc := checkShapes(dir, nil, &out, &errOut); rc != exitError {
		t.Fatalf("a stale decline exited %d, want %d\n%s%s", rc, exitError, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "stale") ||
		!strings.Contains(errOut.String(), "servers.*.per_volume_constraint.l_ssd") {
		t.Errorf("the stale decline was not named:\n%s", errOut.String())
	}
}

// A decline for a field the comparison could not reach is not stale.
//
// Offline, the store is empty: ListServers answers no element, so a field
// under servers[] is skipped rather than compared, and a decline for it
// excuses nothing without being wrong — it is the live field gate's to judge.
// Before this rule, the first element-level decline (Outscale's
// Nics[].PrivateIps[].LinkPublicIp) turned this gate red as stale while the
// decision it records was alive.
func TestAnUnevaluableElementDeclineIsNotStale(t *testing.T) {
	const listServersOp = "GET /instance/v1/zones/fr-par-1/servers"
	declineScaleway(t, emulator.FieldDecline{
		Operation: listServersOp,
		Path:      "servers[].invented_for_this_test",
		Reason:    aReason,
	})
	dir := t.TempDir()
	writeShapeFileFor(t, dir, listServersOp,
		transcript.Field{Path: "servers", Type: "array"},
		transcript.Field{Path: "servers[]", Type: "object"},
		transcript.Field{Path: "servers[].invented_for_this_test", Type: "string"},
	)

	var out, errOut strings.Builder
	if rc := checkShapes(dir, nil, &out, &errOut); rc != exitOK {
		t.Fatalf("an unevaluable element decline exited %d, want %d\n%s%s", rc, exitOK, out.String(), errOut.String())
	}
}

// A decline spelled for the live field gate — the mounted operation name,
// "ipam/v1/API.ListIPs" — is not this gate's to judge: it joins on catalogue
// keys, and an entry it cannot resolve excuses nothing here by construction.
// Before this rule, the first decline written for the live gate turned this
// gate red as "stale" on a decision that was alive and doing its job one gate
// over.
func TestADeclineSpelledForTheLiveGateIsNotStaleHere(t *testing.T) {
	declineScaleway(t, emulator.FieldDecline{
		Operation: "ipam/v1/API.ListIPs",
		Path:      "ips[].source.zonal",
		Reason:    aReason,
	})
	dir := t.TempDir()
	writeShapeFile(t, dir,
		transcript.Field{Path: "servers", Type: "object"},
		transcript.Field{Path: "servers.DEV1-S", Type: "object"},
		transcript.Field{Path: "servers.DEV1-S.ncpus", Type: "number"},
	)

	var out, errOut strings.Builder
	if rc := checkShapes(dir, nil, &out, &errOut); rc != exitOK {
		t.Fatalf("a live-gate decline exited %d here, want %d\n%s%s", rc, exitOK, out.String(), errOut.String())
	}
}

// A field decline with a reason that carries no decision is refused before
// anything is compared, under the same guard Declined() faces. Without this
// call site the guard would exist and gate nothing — the defect
// TestCoverageRefusesAPackWithUnusableRefusals exists for, one level down.
func TestAnUnusableFieldDeclineReasonFailsTheGate(t *testing.T) {
	declineScaleway(t, emulator.FieldDecline{
		Operation: productsServersOp,
		Path:      "servers.*.per_volume_constraint.l_ssd",
		Reason:    "TODO",
	})
	var out, errOut strings.Builder
	if rc := checkShapes(t.TempDir(), nil, &out, &errOut); rc != exitError {
		t.Fatalf("an unusable reason exited %d, want %d\n%s", rc, exitError, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no usable reason") {
		t.Errorf("the unusable reason was not named:\n%s", errOut.String())
	}
}

// The repository's own state passes its own gate: every field the recorded
// shapes prove the clouds return is either served or declined with a reason.
// This is what puts `feint shapes --check` inside `mise run check` — go test
// runs it with no network and no credential — and it proves the instrument ran
// by refusing a zero comparison count, because a gate that compared nothing
// reports success indistinguishably from one that worked.
func TestTheRepositorysShapesAreServedOrDeclined(t *testing.T) {
	var out, errOut strings.Builder
	if rc := checkShapes(filepath.Join("..", "..", "shapes"), nil, &out, &errOut); rc != exitOK {
		t.Fatalf("the committed shapes exited %d, want %d\n%s%s", rc, exitOK, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "\n0 operation(s) compared") {
		t.Fatalf("the gate compared nothing, which proves nothing:\n%s", out.String())
	}
}

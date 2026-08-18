package outscale_test

import (
	"net/http"
	"testing"
)

// Every test here asks with NON-default values, and that is the point, not a
// detail: #276's fields shipped as constants because the suite only ever
// created machines with defaults, under which a constant and a datum are
// indistinguishable — the same lesson #268 taught for Placement one release
// earlier.

// vmOf digs the first Vm out of a Vms answer.
func vmOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	vms, _ := out["Vms"].([]any)
	if len(vms) == 0 {
		t.Fatalf("no Vms in the answer: %v", out)
	}
	vm, _ := vms[0].(map[string]any)
	return vm
}

func singleVmOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	vm, ok := out["Vm"].(map[string]any)
	if !ok {
		t.Fatalf("no Vm in the answer: %v", out)
	}
	return vm
}

func assertVmOptions(t *testing.T, vm map[string]any, want map[string]any, when string) {
	t.Helper()
	for field, expected := range want {
		if got := vm[field]; got != expected {
			t.Errorf("%s: %s = %v, want %v", when, field, got, expected)
		}
	}
}

// TestAVmReadsBackItsOwnOptions is #276 verbatim: the client asks
// medium/restart/legacy and the 200 — the create's own, and every ReadVms
// after it — must answer medium/restart/legacy, not high/stop/uefi. The
// VmType carries no performance flag, so the Performance parameter is the
// one that decides.
func TestAVmReadsBackItsOwnOptions(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1","BootOnCreation":false,`+
			`"BootMode":"legacy","Performance":"medium","VmInitiatedShutdownBehavior":"restart",`+
			`"TpmEnabled":true,`+
			`"ShutdownBehaviorConfiguration":{"GuestAction":"terminate","HostAction":"stop"},`+
			`"ActionsOnNextBoot":{"SecureBoot":"enable"}}`)
	want := map[string]any{
		"BootMode":                    "legacy",
		"Performance":                 "medium",
		"VmInitiatedShutdownBehavior": "restart",
		"TpmEnabled":                  true,
	}
	assertVmOptions(t, vmOf(t, created), want, "the create's own answer")
	id := firstVMID(t, created)

	read := call(t, ts, doc, "ReadVms", `{"Filters":{"VmIds":["`+id+`"]}}`)
	vm := vmOf(t, read)
	assertVmOptions(t, vm, want, "ReadVms")

	sbc, _ := vm["ShutdownBehaviorConfiguration"].(map[string]any)
	if sbc["GuestAction"] != "terminate" || sbc["HostAction"] != "stop" {
		t.Errorf("ShutdownBehaviorConfiguration = %v, want terminate/stop", sbc)
	}
	boot, _ := vm["ActionsOnNextBoot"].(map[string]any)
	if boot["SecureBoot"] != "enable" {
		t.Errorf("ActionsOnNextBoot = %v, want SecureBoot enable", boot)
	}
}

// TestAVmCreatedWithDefaultsReadsThePlatformOnes pins the other direction: a
// bare create answers the platform defaults, HostAction restart among them —
// the SDK's own "By default" line (client.gen.go:9236), which the old
// constant contradicted with "stop".
func TestAVmCreatedWithDefaultsReadsThePlatformOnes(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1","BootOnCreation":false}`)
	vm := vmOf(t, created)
	assertVmOptions(t, vm, map[string]any{
		"BootMode":                    "uefi",
		"Performance":                 "high",
		"VmInitiatedShutdownBehavior": "stop",
		"TpmEnabled":                  false,
		"IsSourceDestChecked":         true,
	}, "a bare create")
	sbc, _ := vm["ShutdownBehaviorConfiguration"].(map[string]any)
	if sbc["GuestAction"] != "stop" || sbc["HostAction"] != "restart" {
		t.Errorf("ShutdownBehaviorConfiguration = %v, want the SDK defaults stop/restart", sbc)
	}
	boot, _ := vm["ActionsOnNextBoot"].(map[string]any)
	if boot["SecureBoot"] != "none" {
		t.Errorf("ActionsOnNextBoot = %v, want SecureBoot none", boot)
	}
}

// TestTheVmTypePerformanceFlagWins holds upstream's precedence: "this
// parameter is ignored if you specify a performance flag directly in the
// VmType parameter" (client.gen.go:3059). p3 spells medium, so a client
// asking highest beside a p3 type reads medium back.
func TestTheVmTypePerformanceFlagWins(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p3","BootOnCreation":false,"Performance":"highest"}`)
	assertVmOptions(t, vmOf(t, created), map[string]any{"Performance": "medium"},
		"a p3 type beside Performance=highest")
}

// TestUpdateVmMovesTheOptions covers the update half upstream declares:
// Performance, VmInitiatedShutdownBehavior, ShutdownBehaviorConfiguration and
// IsSourceDestChecked on UpdateVmRequest — accepted-and-denied there is the
// same non-converging plan one call later.
func TestUpdateVmMovesTheOptions(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1","BootOnCreation":false,"Performance":"medium"}`)
	id := firstVMID(t, created)

	updated := call(t, ts, doc, "UpdateVm",
		`{"VmId":"`+id+`","Performance":"highest","VmInitiatedShutdownBehavior":"terminate",`+
			`"IsSourceDestChecked":false,`+
			`"ShutdownBehaviorConfiguration":{"GuestAction":"terminate"}}`)
	want := map[string]any{
		"Performance":                 "highest",
		"VmInitiatedShutdownBehavior": "terminate",
		"IsSourceDestChecked":         false,
	}
	assertVmOptions(t, singleVmOf(t, updated), want, "the update's own answer")

	read := call(t, ts, doc, "ReadVms", `{"Filters":{"VmIds":["`+id+`"]}}`)
	vm := vmOf(t, read)
	assertVmOptions(t, vm, want, "ReadVms after the update")
	sbc, _ := vm["ShutdownBehaviorConfiguration"].(map[string]any)
	if sbc["GuestAction"] != "terminate" || sbc["HostAction"] != "restart" {
		t.Errorf("ShutdownBehaviorConfiguration = %v, want terminate and the untouched default restart", sbc)
	}

	// A retype onto a flagged VmType re-resolves the performance: the flag
	// wins over the highest stored just above.
	retyped := call(t, ts, doc, "UpdateVm", `{"VmId":"`+id+`","VmType":"tinav6.c2r2p2"}`)
	assertVmOptions(t, singleVmOf(t, retyped), map[string]any{"Performance": "high"},
		"a retype onto a p2 flag")
}

// TestVmOptionsOutsideTheirEnumAreRefused: refused, never stored — a stored
// value outside the platform's enum can only be restituted as a lie or
// silently replaced by a default the client did not ask.
func TestVmOptionsOutsideTheirEnumAreRefused(t *testing.T) {
	ts := newServer(t)

	cases := []struct {
		label string
		body  string
	}{
		{"BootMode", `{"ImageId":"ami-00000001","BootMode":"bios"}`},
		{"Performance", `{"ImageId":"ami-00000001","Performance":"turbo"}`},
		{"VmInitiatedShutdownBehavior", `{"ImageId":"ami-00000001","VmInitiatedShutdownBehavior":"hibernate"}`},
		{"GuestAction", `{"ImageId":"ami-00000001","ShutdownBehaviorConfiguration":{"GuestAction":"restart"}}`},
		{"SecureBoot", `{"ImageId":"ami-00000001","ActionsOnNextBoot":{"SecureBoot":"maybe"}}`},
	}
	for _, tc := range cases {
		status, out := post(t, ts, "CreateVms", tc.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s outside its enum answered %d, want 400 (%v)", tc.label, status, out)
		}
	}
	// Nothing half-created behind a refusal.
	status, read := post(t, ts, "ReadVms", `{}`)
	if status != http.StatusOK {
		t.Fatalf("ReadVms: status %d", status)
	}
	if vms, _ := read["Vms"].([]any); len(vms) != 0 {
		t.Fatalf("a refused create left %d Vm(s) behind", len(vms))
	}
}

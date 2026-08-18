package outscale

import (
	"net/http"
	"strings"

	"github.com/stephrobert/feint/internal/core/resource"
)

// The Vm options a client writes and the reads must restitute (#276).
//
// BootMode, Performance and VmInitiatedShutdownBehavior were accepted at
// create with a 200 while every read answered a constant of the pack — the
// client asked medium/restart/legacy, the same create's answer said
// high/stop/uefi. That is #268's accepted-then-constant pattern on per-machine
// scalars: a Terraform stack setting any of them re-plans the same in-place
// change for ever. The chain #275 established for Placement applies unchanged:
// request → store → response, never request → constant → response.
//
// The same sweep covers the neighbours with the same symptom:
// ShutdownBehaviorConfiguration (both actions were the constant "stop" while
// their own SDK comments state the defaults, GuestAction stop and HostAction
// restart), TpmEnabled, ActionsOnNextBoot.SecureBoot, and UpdateVm's
// IsSourceDestChecked.
//
// Field sources, osc-sdk-go pkg/osc/client.gen.go: CreateVmsRequest.BootMode
// :3024, .Performance :3060 ("ignored if you specify a performance flag
// directly in the VmType parameter"), .VmInitiatedShutdownBehavior :3088
// (stop|restart|terminate), .ShutdownBehaviorConfiguration :3075,
// .TpmEnabled :3081, .ActionsOnNextBoot :3018; UpdateVmRequest.Performance
// :10320, .VmInitiatedShutdownBehavior :10336, .ShutdownBehaviorConfiguration
// :10326, .IsSourceDestChecked :10310, .ActionsOnNextBoot :10295. Enums:
// BootMode :79-84 (legacy|uefi), CreateVmsRequestPerformance :225-230
// (medium|high|highest), ShutdownBehaviorConfigurationGuestAction :769-773
// (stop|terminate), ShutdownBehaviorConfigurationHostAction :795-799
// (restart|stop), SecureBootAction (enable|disable|setup-mode|none) via
// ActionsOnNextBoot :1507-1510.
//
// What is served is the datum; what is enacted is stated in docs/limits.md:
// no guest-initiated shutdown, secure boot or vTPM exists in the runtime, so
// the behavioural half of these fields has nothing to act on here.

// The defaults a Vm created without options reads back. BootMode and
// Performance are what a real machine answered in the recorded account (the
// transcript diff that added the twenty fixed fields); the shutdown defaults
// are the SDK's own "By default" lines — GuestAction stop, HostAction restart
// (client.gen.go:9233-9237) — and VmInitiatedShutdownBehavior stop (the
// documented stop|restart|terminate family's neutral member, matching
// GuestAction's default).
const (
	defaultBootMode           = "uefi"
	defaultPerformance        = "high"
	defaultShutdownBehavior   = "stop"
	defaultGuestAction        = "stop"
	defaultHostAction         = "restart"
	defaultSecureBootAction   = "none"
	attrBootMode              = "BootMode"
	attrPerformance           = "Performance"
	attrShutdownBehavior      = "VmInitiatedShutdownBehavior"
	attrShutdownConfiguration = "ShutdownBehaviorConfiguration"
	attrTpmEnabled            = "TpmEnabled"
	attrActionsOnNextBoot     = "ActionsOnNextBoot"
	attrSourceDestChecked     = "IsSourceDestChecked"
)

type shutdownBehaviorConfigurationRequest struct {
	GuestAction *string `json:"GuestAction"`
	HostAction  *string `json:"HostAction"`
}

type actionsOnNextBootRequest struct {
	SecureBoot *string `json:"SecureBoot"`
}

// vmOptionsRequest is the slice of CreateVms and UpdateVm both carry; BootMode
// is create-only upstream (UpdateVmRequest declares none) and lives beside the
// embedding structs.
type vmOptionsRequest struct {
	Performance                   *string                               `json:"Performance"`
	VmInitiatedShutdownBehavior   *string                               `json:"VmInitiatedShutdownBehavior"`
	ShutdownBehaviorConfiguration *shutdownBehaviorConfigurationRequest `json:"ShutdownBehaviorConfiguration"`
	ActionsOnNextBoot             *actionsOnNextBootRequest             `json:"ActionsOnNextBoot"`
}

// validVmOptions checks the enumerated values CreateVms and UpdateVm share,
// answering the client itself so the two paths cannot drift — the same shape
// as validVmFields, for the same reason. A value outside its enum is refused,
// never stored: stored, every later read would either restitute a value the
// platform does not define or fall back to a default the client did not ask.
func (p *Pack) validVmOptions(w http.ResponseWriter, opts vmOptionsRequest) bool {
	if opts.Performance != nil && !oneOf(*opts.Performance, "medium", "high", "highest") {
		p.badRequest(w, "Performance must be medium, high or highest")
		return false
	}
	if opts.VmInitiatedShutdownBehavior != nil && !oneOf(*opts.VmInitiatedShutdownBehavior, "stop", "restart", "terminate") {
		p.badRequest(w, "VmInitiatedShutdownBehavior must be stop, restart or terminate")
		return false
	}
	if sbc := opts.ShutdownBehaviorConfiguration; sbc != nil {
		if sbc.GuestAction != nil && !oneOf(*sbc.GuestAction, "stop", "terminate") {
			p.badRequest(w, "ShutdownBehaviorConfiguration.GuestAction must be stop or terminate")
			return false
		}
		if sbc.HostAction != nil && !oneOf(*sbc.HostAction, "restart", "stop") {
			p.badRequest(w, "ShutdownBehaviorConfiguration.HostAction must be restart or stop")
			return false
		}
	}
	if boot := opts.ActionsOnNextBoot; boot != nil && boot.SecureBoot != nil &&
		!oneOf(*boot.SecureBoot, "enable", "disable", "setup-mode", "none") {
		p.badRequest(w, "ActionsOnNextBoot.SecureBoot must be enable, disable, setup-mode or none")
		return false
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, v := range allowed {
		if value == v {
			return true
		}
	}
	return false
}

// storeVmOptions writes what the update carries over what the create resolved,
// field by field: a request that says nothing about a field leaves the stored
// datum alone.
func storeVmOptions(res *resource.Resource, opts vmOptionsRequest, vmType string) {
	res.Attrs[attrPerformance] = resolvedVmPerformance(vmType, opts.Performance, vmPerformance(res))
	if opts.VmInitiatedShutdownBehavior != nil {
		res.Attrs[attrShutdownBehavior] = *opts.VmInitiatedShutdownBehavior
	}
	if sbc := opts.ShutdownBehaviorConfiguration; sbc != nil {
		stored := vmShutdownConfiguration(res)
		if sbc.GuestAction != nil {
			stored["GuestAction"] = *sbc.GuestAction
		}
		if sbc.HostAction != nil {
			stored["HostAction"] = *sbc.HostAction
		}
		res.Attrs[attrShutdownConfiguration] = stored
	}
	if boot := opts.ActionsOnNextBoot; boot != nil && boot.SecureBoot != nil {
		res.Attrs[attrActionsOnNextBoot] = map[string]any{"SecureBoot": *boot.SecureBoot}
	}
}

// resolvedVmPerformance is the performance a Vm's reads will answer.
//
// The flag inside the VmType wins, which is upstream's own rule: "this
// parameter is ignored if you specify a performance flag directly in the
// VmType parameter" (client.gen.go:3059). Then the client's explicit ask,
// then what is already stored, then the platform default.
func resolvedVmPerformance(vmType string, asked *string, current string) string {
	if flag := performanceFlagOf(vmType); flag != "" {
		return flag
	}
	if asked != nil {
		return *asked
	}
	return orDefault(current, defaultPerformance)
}

// performanceFlagOf reads the pZ of a tinavW.cXrYpZ type name. The mapping is
// Outscale's published one (the VM Types page the SDK's own VmType comment
// points to): p1 highest, p2 high, p3 medium. A type without the flag — the
// tinavW.cXrY spelling, or an AWS name — carries none.
func performanceFlagOf(vmType string) string {
	if !strings.HasPrefix(vmType, "tinav") {
		return ""
	}
	switch {
	case strings.HasSuffix(vmType, "p1"):
		return "highest"
	case strings.HasSuffix(vmType, "p2"):
		return "high"
	case strings.HasSuffix(vmType, "p3"):
		return "medium"
	}
	return ""
}

// The readers every door goes through: the stored datum, else the default a
// machine created before these fields were stored has always published. One
// reader per field so no view rebuilds the fallback chain its own way.

func vmBootMode(res *resource.Resource) string {
	return orDefault(stringOf(res.Attrs[attrBootMode]), defaultBootMode)
}

func vmPerformance(res *resource.Resource) string {
	return orDefault(stringOf(res.Attrs[attrPerformance]), defaultPerformance)
}

func vmShutdownBehavior(res *resource.Resource) string {
	return orDefault(stringOf(res.Attrs[attrShutdownBehavior]), defaultShutdownBehavior)
}

// vmShutdownConfiguration always answers both actions: the SDK declares the
// defaults on the schema itself, and a partial object would read as an
// omission to a client that decodes pointers.
func vmShutdownConfiguration(res *resource.Resource) map[string]any {
	out := map[string]any{
		"GuestAction": defaultGuestAction,
		"HostAction":  defaultHostAction,
	}
	if stored, ok := res.Attrs[attrShutdownConfiguration].(map[string]any); ok {
		for k, v := range stored {
			out[k] = v
		}
	}
	return out
}

func vmActionsOnNextBoot(res *resource.Resource) map[string]any {
	if stored, ok := res.Attrs[attrActionsOnNextBoot].(map[string]any); ok {
		if action := stringOf(stored["SecureBoot"]); action != "" {
			return map[string]any{"SecureBoot": action}
		}
	}
	return map[string]any{"SecureBoot": defaultSecureBootAction}
}

func vmTpmEnabled(res *resource.Resource) bool {
	enabled, _ := res.Attrs[attrTpmEnabled].(bool)
	return enabled
}

// vmSourceDestChecked defaults to true, the value the pack has always
// published and the platform's own default for a Vm in a Net.
func vmSourceDestChecked(res *resource.Resource) bool {
	if stored, ok := res.Attrs[attrSourceDestChecked].(bool); ok {
		return stored
	}
	return true
}

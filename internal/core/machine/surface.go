package machine

// The surface a provider pack may reach, declared rather than discovered
// (#511).
//
// Three packs used to reach twenty-nine sites of this package — sixteen direct
// driver calls and thirteen interface assertions — and nothing said which of
// them were legitimate. The corridor was opened first: GroupSync took the
// firewall orchestration (#509), Plan and Reconciler took the interface plan
// and the hot verbs (#510), EnsureBackingNetwork and RemoveBackingNetwork
// closed the network family, and the shared recorder gave all three a common
// witness (#515). What was left was the door, and this is it.
//
// # What a pack may ask of the runtime
//
// The eight families below, and nothing else. The list is short because the
// question it answers is narrow: *what does a provider have to ask a host to
// do that only a host can do?* Boot a machine and stop it. Put it on a
// network, and take the network away. Make an address reach it. Enforce a rule
// set. Keep two networks apart. Distribute a balancer. Everything else a pack
// does — deciding what its API says, which fields carry which value, when a
// state changes — needs no host at all.
//
// # What it excludes, which is the point
//
// A surface that admits everything the packs reach today would be an
// inventory. This one excludes, and internal/cli's
// TestTheDeclaredDriverSurfaceIsSmallerThanThePackage asserts the exclusions
// by name rather than leaving them to absence:
//
//   - Driver itself and every implementation of it. A pack holding one calls
//     Start, Remove or RemoveNetwork past Binding.ours and past the driver's
//     mustOwn — the path a crafted snapshot walked through, and the reason
//     Resource.Runtime is untrusted input.
//   - Its optional halves — Router, Firewaller, Peerer, Isolator, Balancer.
//     Reaching one by type assertion bypasses the shared layer as surely as
//     calling a method does; that correction is what took the measurement of
//     this defect from eleven sites to twenty-nine.
//   - The driver's own argument vocabulary — Spec, NetworkSpec, AddressSpec,
//     FirewallBinding — which the layer builds from what a pack declares.
//   - Thirteen of Binding's own verbs, including Start, Stop, RouteAddress and
//     SyncRuleSet: they are the mechanics the two orchestrators sequence.
//     Binding.PowerOn is excluded and Reconciler.PowerOn admitted, and the
//     difference is the whole of #510 — the order addresses, then memberships,
//     then the firewall is a property of the runtime, and a pack that boots
//     through the binding skips it.
//   - GroupSync.AfterBoot, for the same reason one level up: the Reconciler
//     runs it, last.
//
// Measured on 2026-08-26, the day the door closed: this package exports 97
// package-level names and 297 members of them; the list below admits 17 and 41.
// Binding alone offers 36 exported members and 14 are in — the other 22 are the
// mechanics. What the three packs actually reach is 216 sites, every one of them
// named here and none of them a driver.
//
// # The rule when something is missing
//
// A gesture this list lacks is added to it — the driver gains a service — and
// never worked around. That is the maintainer's own wording, and it is why
// this file grew Binding.ReconcileIsolation and the three balancer verbs in
// the same change that closed the door: two packs were reaching the isolation
// pass with a raw driver and one was asserting its way to the balancing half,
// and the answer to both was a service, not an exemption.
//
// # What holds this
//
// internal/cli's TestNoPackReachesPastTheDeclaredDriverSurface reads the packs'
// own sources against this list and names the pack, the gesture and the line.
// It follows a value across assignments, so a bypass one local variable deep is
// caught too. Its exemption ledger is empty today, and an exemption without a
// written reason is refused by TestEveryBarrageExemptionSaysWhy.
//
// The compiler holds the rest and holds it harder: emulator.Env carries no
// Driver a pack can read, and Binding's driver field is unexported behind
// WithDriver, so `p.binding().Driver.EnsureNetwork(…)` — a sentence that
// compiled before this change — no longer does.

// PackSurface is the closed list of what a provider pack may name in this
// package: package-level names as they are written (machine.Attachment), and
// members of those types as "Type.Member". The value says what a pack asks it
// for.
//
// It is a function rather than a variable so no caller can add to it at
// runtime, which is the same reason Declined() is one.
func PackSurface() map[string]string {
	return map[string]string{
		// S1 — machine lifecycle. What the pack declares about its machines,
		// and the four verbs that move one. Serialise and Transition are here
		// because the store mutation and the slow runtime effect have to be
		// ordered together: a per-target lock the pack held itself is how two
		// concurrent poweron left an orphan container.
		"Binding":                  "the pack's own declaration of its machines: prefix, login, the keys it publishes under",
		"Binding.Serialise":        "serialise two actions on one target, since the runtime call runs outside the store lock",
		"Binding.Transition":       "change a stored resource under the shared conditional write-back",
		"Binding.Observe":          "read a resource back after an effect, with the same write-back discipline",
		"Binding.PowerOff":         "stop the machine behind a resource",
		"Binding.Destroy":          "destroy the machine behind a resource",
		"Binding.RefreshIfRunning": "re-read what the host holds for a machine the store calls running",
		"Binding.AddressOf":        "the address the boot produced, as this pack publishes it",
		"Binding.RuntimeKey":       "the pack's own Runtime key, to read the machine name it is about to hand to Unroute",
		"Boot":                     "what to boot: image, login, keys, user data",
		"Image":                    "one entry of the pack's image table, the runtime image and the login together",
		"Image.Ref":                "the image identifier that table resolved to",
		"Image.User":               "the login that image provisions",

		// S2 — the interface plan. The pack declares the shape; the layer
		// executes the one order.
		"Reconciler":           "the orchestrator that executes a declared plan in the runtime's order",
		"Reconciler.PowerOn":   "start a machine on its plan and replay the post-boot order",
		"Plan":                 "the machine's declared interface shape",
		"Plan.Memberships":     "the networks joined after boot, read back from the plan the pack built",
		"Attachment":           "one interface: a network, an address, the mask, the secondaries",
		"Attachment.PrefixLen": "the mask of an attachment the pack assembled",

		// S3 — public addresses.
		"Reconciler.Route":           "make one public address reach the machine, the hot half",
		"Reconciler.Unroute":         "take the route back, machine gone or not",
		"Reconciler.ReplayAddresses": "hand a machine its promised addresses again after a boot",

		// S4 — networks. Ensure records the name only once the driver accepted
		// it, which is the ordering two packs had wrong.
		"BackingNetwork":               "what an emulated subnet needs from the runtime, in the pack's terms",
		"Binding.EnsureBackingNetwork": "give an emulated subnet a real network and record its name",
		"Binding.RemoveBackingNetwork": "take that network away and forget its name",
		"DefaultMachineNetwork":        "the emulator's own network, the one placement a pack may ask for with no subnet of its own",

		// S5 — membership, the hot attach a cloud performs on a running
		// machine, which cannot fold into a boot-time plan.
		"Reconciler.Join":  "put a running machine on one more network, firewall resync included",
		"Reconciler.Leave": "take it off again",

		// S6 — rule sets. The pack translates; the skeleton sequences.
		"GroupSync":                           "the firewall orchestrator, built from the pack's own translation of its groups",
		"GroupSync.SyncGroup":                 "write one group's rule set and replay it onto every wearer",
		"GroupSync.ApplyMachine":              "write every set one resource wears and attach them in one call",
		"GroupSync.SyncReferrers":             "re-expand the groups whose rules name the groups this resource wears",
		"GroupSync.Drop":                      "remove a deleted group's rule set from the runtime",
		"FirewallSpec":                        "one rule set as the runtime takes it, translated by the pack",
		"FirewallSpec.Rules":                  "the rules of a set the pack is assembling",
		"FirewallSpec.DefaultEgress":          "the set's default egress action, which upstream models differ on",
		"FirewallSpec.WithPermissiveCatchAll": "the shared catch-all a permissive provider needs, rather than a fourth copy",
		"FirewallRule":                        "one rule, in the neutral shape every runtime takes",
		"FirewallRule.Direction":              "a rule's direction, read back from what the pack built",
		"FirewallRule.Action":                 "a rule's action, likewise",
		"FirewallRule.Source":                 "a rule's source block, likewise",
		"FirewallRule.Destination":            "a rule's destination block, likewise",
		"FirewallRule.PortFrom":               "the low port of a rule the pack is assembling",
		"FirewallRule.PortTo":                 "the high port of a rule the pack is assembling",
		"FirewallName":                        "the runtime name of a pack's rule set, spelled once for the three",

		// S7 — isolation and peering, with one writer. The pack owns the
		// predicate — what "may reach" means in its upstream model — and
		// nothing else.
		"Binding.ReconcileIsolation": "apply what every member may reach, over the whole set, through the one writer",
		"IsolationMember":            "one network of this pack in a reconciliation pass",

		// S8 — the balancer, the one family whose delivery is partial often
		// enough that the effect is reported beside the intent.
		"Binding.Balances":          "ask whether this runtime both implements and declares balancing",
		"Binding.EnsureBalancer":    "hand the whole balancer to the runtime",
		"Binding.RemoveBalancer":    "withdraw a balancer from the runtime",
		"BalancerSpec":              "a named balancer on one network, described whole",
		"BalancerSpec.Listen":       "the address a balancer the pack assembled answers on",
		"BalancerSpec.Listeners":    "the ports a balancer the pack assembled answers on",
		"BalancerSpec.Targets":      "the backends behind it",
		"BalancerListener":          "one port a balancer answers on, and the backend port",
		"BalancerDelivery.Lines":    "the two strings the delivery is recorded as",
		"RecordBalancerDelivery":    "write what the hand-off produced onto the pack's own resource",
		"ErrBalancerNotDistributed": "tell a limit from an incident, so a shape this runtime will never take is not an error",
	}
}

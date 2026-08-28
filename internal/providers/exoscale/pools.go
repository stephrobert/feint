package exoscale

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/serialise"
)

// Instance pools: one control-plane write that creates or destroys several
// machines (EXO-7, #12's sibling, #232).
//
// # Why this was its own batch, and what dissolved the difficulty
//
// The issue deferred it with a real argument: every mechanism the machine layer
// holds is written for **one named target**. `Binding.Serialise` keys its lock
// by one identifier, `Binding.Name` derives one machine name from one resource
// id, `storetest.Orphans` asks who owns a resource, singular. A pool maps to
// *n*, so folding it into a triage would have meant either shipping a control
// plane that answers about machines it never starts — the lie this project
// exists to avoid — or reworking the machine layer inside somebody else's issue.
//
// It is neither, because a pool does not own machines. **It owns instances, and
// an instance owns a machine.** That is upstream's own model — their pool schema
// carries `instances`, and `list-instances` shows the members like any other
// instance — and it is the reading that keeps every per-target mechanism intact:
//
//   - each member is an ordinary `compute-instance` resource, so it goes through
//     the same create path, the same `p.start`, the same `Binding.PowerOn`, with
//     its own serialisation on its own name;
//   - the pool stores nothing about machines. It is scaled by creating or
//     deleting members, and the runtime follows because the members' own path is
//     the one that touches it;
//   - `Owns` declares the member-to-pool relation, so a member left naming a
//     pool that is gone is a finding rather than a leak.
//
// What the pool does own is the arithmetic — *how many members should exist* —
// and one lock, keyed by the pool, so two scales cannot both read three and both
// decide to add one. That is the discipline `serialise` exists for, applied at
// the level that actually has a race, and it is the answer to the issue's fourth
// closing condition.
//
// # The one thing a scale must never be
//
// `scale` answers after the members exist or are gone, never before. The
// machines behind them start asynchronously — that is `PowerOn`'s contract, and
// a container takes seconds — so what this returns is the truth about the
// control plane, and `capabilities.machines` is what says whether anything is
// behind it. A pool that answered `size: 3` while holding one member would be
// exactly the failure mode the issue names.

const (
	kindPool = "instance-pool"
	nounPool = "instance-pool"

	// The states upstream declares for a pool. `running` is the steady one;
	// `scaling-up` and `scaling-down` are transient and this emulator does not
	// pass through them, for the reason limits.md gives about every other
	// transition: an emulated wait is a wait invented for realism.
	poolRunning = "running"

	// runtimePoolKey links a member instance to the pool that made it. In
	// Runtime rather than Attrs, like every other piece of this emulator's own
	// bookkeeping: the instance view is the API's shape, and upstream's instance
	// schema declares a `manager`, which is what the view publishes from it.
	runtimePoolKey = "instance-pool"
)

// lockPool serialises the arithmetic of one pool.
//
// Keyed by the pool's own id rather than globally: two pools scaling at once are
// independent, and a global lock would put every pool behind the slowest one —
// the mistake CLAUDE.md names about the address lock. What it protects is
// read-modify-write over the member count, which is the only race a pool has:
// two concurrent scales that both read three members would both create one.
func (p *Pack) lockPool(id string) func() { return serialise.Lock(Name + "/pool/" + id) }

// poolView publishes a pool in the shape the OpenAPI declares. `instances` is
// computed from the store at read time, so a member deleted by any path — an
// evict, a scale down, or `exo compute instance delete` on the member itself —
// leaves the list without anybody maintaining it.
func (p *Pack) poolView(res *resource.Resource) map[string]any {
	// No created-at, unlike every other view in this pack: their instance-pool
	// schema does not declare one, and the contract check said so — "created-at:
	// instance-pool does not define this field". The schema is enforced as
	// closed, so a field they do not declare is a field a client would not
	// expect, however natural it looks beside the others.
	out := map[string]any{
		"id":        res.ID,
		"state":     res.State,
		"instances": p.poolMemberRefs(res.ID),
	}
	for key, value := range res.Attrs {
		out[key] = value
	}
	return out
}

// poolMembers answers the instances a pool manages, oldest first.
//
// The order matters for one caller: scaling down removes the newest, which is
// the only choice that does not surprise a client — the machine that has been
// serving longest is the one it would least expect to lose.
func (p *Pack) poolMembers(poolID string) []*resource.Resource {
	list := p.env.Store.List(kindInstance, resource.Tenant{Provider: Name})
	members := make([]*resource.Resource, 0, len(list))
	for _, res := range list {
		if res.Runtime[runtimePoolKey] == poolID {
			members = append(members, res)
		}
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].Created.Equal(members[j].Created) {
			return members[i].ID < members[j].ID
		}
		return members[i].Created.Before(members[j].Created)
	})
	return members
}

func (p *Pack) poolMemberRefs(poolID string) []any {
	members := p.poolMembers(poolID)
	refs := make([]any, 0, len(members))
	for _, res := range members {
		refs = append(refs, map[string]any{"id": res.ID})
	}
	return refs
}

type poolRequest struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Size           *int64            `json:"size"`
	InstancePrefix string            `json:"instance-prefix"`
	DiskSize       *int64            `json:"disk-size"`
	UserData       string            `json:"user-data"`
	Labels         map[string]string `json:"labels"`
	MinAvailable   *int64            `json:"min-available"`
	IPv6Enabled    *bool             `json:"ipv6-enabled"`
	InstanceType   *ref              `json:"instance-type"`
	Template       *ref              `json:"template"`
	SSHKey         *namedRef         `json:"ssh-key"`
	// The six the CLI sends on every update and nothing read, until the field
	// gate named them: `exo compute instance-pool update` re-sends the pool's
	// whole shape rather than a diff, so a pool with no security group is still
	// sent `"security-groups": []`. Read and stored, because a client that sends
	// a list and reads back something else has been told its call did nothing.
	AntiAffinityGroups []any     `json:"anti-affinity-groups"`
	SecurityGroups     []any     `json:"security-groups"`
	PrivateNetworks    []any     `json:"private-networks"`
	ElasticIPs         []any     `json:"elastic-ips"`
	SSHKeys            []any     `json:"ssh-keys"`
	DeployTarget       *namedRef `json:"deploy-target"`
	// Sent by the CLI on every call, declared by the schema, and stored so the
	// members inherit it — the same field the template registration dropped
	// until a real client drove it (#174).
	ApplicationConsistentSnapshotEnabled *bool `json:"application-consistent-snapshot-enabled"`
	// What the members do about a public address, declared by their pool
	// schema and read here since #345, because a pool's members are what a
	// load balancer service forwards to and a backend is identified by its
	// public address. Same vocabulary as the instance's own field, and the
	// same default: inet4 unless the client says none.
	PublicIPAssignment string `json:"public-ip-assignment"`
}

type ref struct {
	ID string `json:"id"`
}

type namedRef struct {
	Name string `json:"name"`
}

// createInstancePool records the pool, then fills it.
//
// The members are created inside the pool's lock and started outside the store's:
// `p.start` is the same call an ordinary instance create makes, and it takes
// seconds per machine when a runtime is configured. Holding anything across it
// is the defect CLAUDE.md devotes a section to.
func (p *Pack) createInstancePool(w http.ResponseWriter, r *http.Request) {
	var req poolRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if bad := req.invalid(); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}
	size := int64(1)
	if req.Size != nil {
		size = *req.Size
	}
	if size < 0 {
		writeError(w, http.StatusBadRequest, "size cannot be negative")
		return
	}
	templateID := ""
	if req.Template != nil {
		templateID = req.Template.ID
	}
	if _, found := p.templateByID(templateID); !found {
		writeError(w, http.StatusBadRequest, "the template is not one this zone offers")
		return
	}

	now := p.env.Now()
	pool := resource.New(p.env.NewID(), kindPool, resource.Tenant{Provider: Name}, poolRunning, now)
	pool.Attrs = req.attrs(size)
	p.env.Store.Put(pool)

	unlock := p.lockPool(pool.ID)
	created := p.growPool(r.Context(), pool, size)
	unlock()

	// Started after the lock is released, and after every member exists: a
	// client that reads the pool while the machines boot sees the size it asked
	// for, which is what the control plane really promises. Each boot commits
	// its own result — startPoolMember says why.
	for _, member := range created {
		p.startPoolMember(r.Context(), member.ID)
	}

	p.writeOperation(w, p.operationReferring(nounPool, pool.ID))
}

func (r poolRequest) invalid() string {
	for field, value := range map[string]string{
		"name": r.Name, "description": r.Description, "instance-prefix": r.InstancePrefix,
	} {
		if strings.ContainsAny(value, "\n\r\x00") {
			return field + " carries control characters"
		}
	}
	return ""
}

// attrs is what the pool stores, and it is also what every member inherits.
func (r poolRequest) attrs(size int64) map[string]any {
	attrs := map[string]any{
		"name":            r.Name,
		"description":     r.Description,
		"size":            size,
		"instance-prefix": orDefaultPrefix(r.InstancePrefix),
		"labels":          labelsToAttr(r.Labels),
		"disk-size":       orDefaultDiskSize(r.DiskSize),
		"user-data":       r.UserData,
	}
	if r.Template != nil {
		attrs["template"] = map[string]any{"id": r.Template.ID}
	}
	if r.InstanceType != nil {
		attrs["instance-type"] = map[string]any{"id": r.InstanceType.ID}
	}
	if r.MinAvailable != nil {
		attrs["min-available"] = *r.MinAvailable
	}
	if r.IPv6Enabled != nil {
		attrs["ipv6-enabled"] = *r.IPv6Enabled
	}
	if r.ApplicationConsistentSnapshotEnabled != nil {
		attrs["application-consistent-snapshot-enabled"] = *r.ApplicationConsistentSnapshotEnabled
	}
	// Present and empty rather than absent, which is what the schema declares
	// and what `exo compute instance-pool show` dereferences.
	attrs["public-ip-assignment"] = orDefault(r.PublicIPAssignment, "inet4")
	attrs["anti-affinity-groups"] = listOrEmpty(r.AntiAffinityGroups)
	attrs["security-groups"] = listOrEmpty(r.SecurityGroups)
	attrs["private-networks"] = listOrEmpty(r.PrivateNetworks)
	attrs["elastic-ips"] = listOrEmpty(r.ElasticIPs)
	if len(r.SSHKeys) > 0 {
		attrs["ssh-keys"] = r.SSHKeys
	}
	if r.DeployTarget != nil {
		attrs["deploy-target"] = map[string]any{"id": r.DeployTarget.Name}
	}
	return attrs
}

// listOrEmpty keeps a declared array present. A nil slice marshals to null, and
// a client ranging over null is a client that stops.
func listOrEmpty(list []any) []any {
	if list == nil {
		return []any{}
	}
	return list
}

// orDefaultPrefix is upstream's own default: a member is named after the pool
// when the client names no prefix. It matters more than it looks — the member
// name becomes the machine's hostname, and two pools with the same prefix would
// produce machines a human cannot tell apart.
func orDefaultPrefix(prefix string) string {
	if prefix == "" {
		return "pool"
	}
	return prefix
}

func orDefaultDiskSize(size *int64) int64 {
	if size == nil || *size <= 0 {
		return 50
	}
	return *size
}

// growPool creates the members a pool is missing, and answers them so the caller
// can start them outside the lock.
//
// Called under lockPool. It reads the current members rather than trusting a
// count the caller carried: the whole reason the lock exists is that the two can
// differ between the read and the write.
func (p *Pack) growPool(_ context.Context, pool *resource.Resource, target int64) []*resource.Resource {
	existing := int64(len(p.poolMembers(pool.ID)))
	created := make([]*resource.Resource, 0)
	for i := existing; i < target; i++ {
		// The member's address is chosen and the member stored under one hold
		// of the address lock: freeAddress chooses from what the store holds,
		// so a member allocated but not yet stored is a member whose address
		// the next caller is handed again (#484). Per member rather than
		// around the loop, so a pool of fifty does not hold every other
		// allocation of the pack behind it.
		unlock := p.lockAddresses()
		member := p.newPoolMember(pool, i)
		p.env.Store.Put(member)
		unlock()
		created = append(created, member)
	}
	return created
}

// startPoolMember boots one member and commits what the boot produced — the
// machine name, the address, and the state the effect reached, "error"
// included. The members used to be started on the caller's local copy and
// never written back: the store kept the running state they were created
// with, `running` stood over a member whose launch the host had refused, and
// a member that did start showed "no machine" in its runtime for its whole
// life (#484). Through Transition, so the write-back is conditional and a
// delete landing mid-boot wins.
//
// TestAFailedPoolMemberStartIsPublishedAsError and
// TestAPoolMemberStartIsRecordedInTheStore fail without this.
func (p *Pack) startPoolMember(ctx context.Context, id string) {
	_ = p.binding().Transition(p.env.Store, p.env.Now, kindInstance, id, func(res *resource.Resource) {
		p.start(ctx, res)
	})
}

// newPoolMember builds one member: an ordinary instance, carrying the pool's
// declared shape and a manager reference back to it.
// Called under p.lockAddresses(), held by growPool across the choice and the
// Put: the address below must be in the store before the lock is released.
//
// The member is born stopped, not running: the state a client reads is the one
// the boot produced, and startPoolMember commits that once the machine is up —
// or failed. Born running, a member whose launch the host refused kept reading
// `running` forever, with no machine behind it (#484).
func (p *Pack) newPoolMember(pool *resource.Resource, index int64) *resource.Resource {
	now := p.env.Now()
	member := resource.New(p.env.NewID(), kindInstance, resource.Tenant{Provider: Name}, stoppedState, now)
	prefix, _ := pool.Attrs["instance-prefix"].(string)
	poolName, _ := pool.Attrs["name"].(string)
	if prefix == "pool" && poolName != "" {
		prefix = poolName
	}

	member.Attrs = map[string]any{
		"name":      prefix + "-" + strconv.FormatInt(index+1, 10),
		"disk-size": resource.Int64(pool, "disk-size"),
		"user-data": pool.Attrs["user-data"],
		"labels":    pool.Attrs["labels"],
		// The relation upstream publishes on the instance itself: a client
		// reading a member learns which pool manages it without asking the pool.
		"manager": map[string]any{"id": pool.ID, "type": "instance-pool"},
	}
	if t, ok := pool.Attrs["template"].(map[string]any); ok {
		member.Attrs["template"] = t
	}
	if t, ok := pool.Attrs["instance-type"].(map[string]any); ok {
		member.Attrs["instance-type"] = t
	}
	// The address the API publishes for a member, taken from the pack's own
	// pool under the same lock every other allocation here takes (#345).
	//
	// It was missing, and the gap was invisible until a load balancer service
	// had to name its backends: `load-balancer-server-status` identifies a
	// backend by its public address, so members with none made every service
	// answer an empty backend list — which reads as "this pool has nobody in
	// it" rather than as "nobody measured these". createInstance has assigned
	// one since #202 and said why in a comment this path never read.
	//
	// TestAPoolMemberCarriesThePublicAddressItsPoolDeclares fails without it.
	// No lock of its own: the caller already holds p.lockAddresses() across
	// this choice and the Put that makes it visible (#484).
	if assignment, _ := pool.Attrs["public-ip-assignment"].(string); assignment != "none" {
		if ip, ok := p.freeAddress(); ok {
			member.Attrs["public-ip"] = ip
		}
	}
	// The pool's declared shape the members inherit and upstream publishes on
	// the instance itself: the security groups they wear — the app tier of
	// examples/stacks/exoscale carries its whole firewall this way — and the
	// keys their machines boot with. A member with no group wears the default
	// one, exactly like an instance created directly.
	if ids := poolRefIDs(pool.Attrs["security-groups"]); len(ids) > 0 {
		member.Attrs[attrSecurityGroupIDs] = ids
	} else {
		p.ensureDefaultSecurityGroup()
		member.Attrs[attrSecurityGroupIDs] = []any{defaultSecurityGroupID}
	}
	if keys, ok := pool.Attrs["ssh-keys"]; ok {
		member.Attrs["ssh-keys"] = keys
	}
	// And the pool's private networks, which every member joins. Upstream this
	// is what the pool's declaration means — measured on the real API on
	// 2026-08-26: a member of a pool declaring one network answers
	// `private-networks: [{id, mac-address}]` on get-instance — and it is what
	// makes the membership real here: the boot replays these records onto the
	// machine (reattachPrivateNetworks), and the firewall sync that follows
	// finds an interface to attach the member's rule sets to. Before this the pool stored its list and nothing turned it
	// into memberships: one routed NIC per member, and the app tier's rule set
	// sat at used_by=0 on the host (#492).
	//
	// TestAPoolMemberJoinsThePoolsPrivateNetworks fails without it.
	if atts := p.memberAttachments(pool); len(atts) > 0 {
		member.Attrs[attrInstancePrivateNetworks] = attachmentsToAttr(atts)
	}
	member.Runtime = map[string]string{runtimePoolKey: pool.ID}
	return member
}

// memberAttachments builds the memberships one new member takes: one per
// private network the pool declares, with a lease from the range when the
// network is managed — chosen exactly the way an instance-side ":attach"
// chooses one. Called under p.lockAddresses(), held by growPool across the
// choice and the Put, so two members cannot be handed one address (#484's
// discipline). A network that no longer exists is skipped the way the
// instance view drops it: the relation is computed against what is, and a
// managed range with nothing left is a membership without a lease, logged
// rather than silently absent.
func (p *Pack) memberAttachments(pool *resource.Resource) []pnAttachment {
	ids := poolRefIDs(pool.Attrs["private-networks"])
	out := make([]pnAttachment, 0, len(ids))
	for _, ref := range ids {
		id, _ := ref.(string)
		pn, found := p.env.Store.Get(Name, kindPrivateNetwork, id)
		if !found {
			continue
		}
		ip := ""
		if dhcp, managed := rangeOf(pn); managed {
			ip = firstFreeLease(dhcp, p.takenLeases(pn.ID))
			if ip == "" {
				p.logger().Warn("no address left in the private network's range for a pool member",
					"pool", pool.ID, "private-network", pn.ID)
			}
		}
		out = append(out, pnAttachment{NetworkID: id, IP: ip})
	}
	return out
}

// poolRefIDs reads the ids out of a stored list of {id: …} references,
// tolerating the []any a snapshot restore produces.
func poolRefIDs(v any) []any {
	list, _ := v.([]any)
	out := make([]any, 0, len(list))
	for _, entry := range list {
		ref, _ := entry.(map[string]any)
		if id, _ := ref["id"].(string); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func (p *Pack) listInstancePools(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindPool, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	out := make([]map[string]any, 0, len(list))
	for _, res := range list {
		out = append(out, p.poolView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"instance-pools": out})
}

func (p *Pack) getInstancePool(w http.ResponseWriter, r *http.Request) {
	res, found := p.env.Store.Get(Name, kindPool, r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.poolView(res))
}

// updateInstancePool changes the pool's declared shape, and only the fields the
// client sent — the rule updateInstance states.
//
// It does not resize: upstream has a separate `:scale` for that, and the update
// schema carries no size. Existing members keep the shape they were made with,
// which is upstream's behaviour too: a pool's template changes what the *next*
// member boots, never what a running one is.
func (p *Pack) updateInstancePool(w http.ResponseWriter, r *http.Request) {
	var req poolRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if bad := req.invalid(); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindPool, id, func(stored *resource.Resource) error {
		if req.Name != "" {
			stored.Attrs["name"] = req.Name
		}
		if req.Description != "" {
			stored.Attrs["description"] = req.Description
		}
		if req.InstancePrefix != "" {
			stored.Attrs["instance-prefix"] = req.InstancePrefix
		}
		if req.Labels != nil {
			stored.Attrs["labels"] = labelsToAttr(req.Labels)
		}
		if req.Template != nil {
			stored.Attrs["template"] = map[string]any{"id": req.Template.ID}
		}
		if req.InstanceType != nil {
			stored.Attrs["instance-type"] = map[string]any{"id": req.InstanceType.ID}
		}
		if req.DiskSize != nil {
			stored.Attrs["disk-size"] = *req.DiskSize
		}
		if req.MinAvailable != nil {
			stored.Attrs["min-available"] = *req.MinAvailable
		}
		if req.PublicIPAssignment != "" {
			stored.Attrs["public-ip-assignment"] = req.PublicIPAssignment
		}
		// The lists the CLI re-sends on every update, whether or not they moved.
		// Written when present rather than when non-empty: emptying a pool's
		// security groups is a change a client makes, and treating an empty list
		// as "said nothing" would make that call silently do nothing.
		for field, list := range map[string][]any{
			"anti-affinity-groups": req.AntiAffinityGroups,
			"security-groups":      req.SecurityGroups,
			"private-networks":     req.PrivateNetworks,
			"elastic-ips":          req.ElasticIPs,
		} {
			if list != nil {
				stored.Attrs[field] = list
			}
		}
		if req.SSHKeys != nil {
			stored.Attrs["ssh-keys"] = req.SSHKeys
		}
		if req.DeployTarget != nil {
			stored.Attrs["deploy-target"] = map[string]any{"id": req.DeployTarget.Name}
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounPool, id))
}

type scalePoolRequest struct {
	Size *int64 `json:"size"`
}

// scaleInstancePool moves the number of members, in both directions.
//
// This is the operation the batch exists for, and the closing condition names
// what it must really do: the count the runtime holds moves, not only the number
// the API reports. It does, because a member is an instance and an instance's
// machine is started and terminated by the paths that already exist.
//
// Under the pool's lock, and the read of the current size happens inside it:
// two concurrent scales to four that both read three would otherwise both add
// one and produce five.
//
// TestScalingAPoolMovesTheMembersAndNotTheirNeighbours fails without this.
func (p *Pack) scaleInstancePool(w http.ResponseWriter, r *http.Request) {
	var req scalePoolRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Size == nil || *req.Size < 0 {
		writeError(w, http.StatusBadRequest, "size is required and cannot be negative")
		return
	}
	id := r.PathValue("id")
	pool, found := p.env.Store.Get(Name, kindPool, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	unlock := p.lockPool(id)
	members := p.poolMembers(id)
	var created []*resource.Resource
	var removed []*resource.Resource
	switch {
	case int64(len(members)) < *req.Size:
		created = p.growPool(r.Context(), pool, *req.Size)
	case int64(len(members)) > *req.Size:
		// The newest go first: the machine serving longest is the one a client
		// would least expect to lose.
		removed = members[*req.Size:]
		for _, member := range removed {
			p.env.Store.Delete(Name, kindInstance, member.ID)
		}
	}
	// The declared size follows the same write, so a reader cannot catch the
	// pool announcing one number while holding another.
	_ = p.env.Store.Update(Name, kindPool, id, func(stored *resource.Resource) error {
		stored.Attrs["size"] = *req.Size
		return nil
	})
	unlock()

	// Outside the lock, because both directions touch the runtime and take
	// seconds: starting a container, and terminating one. Each boot commits
	// its own result — startPoolMember says why.
	for _, member := range created {
		p.startPoolMember(r.Context(), member.ID)
	}
	for _, member := range removed {
		p.destroy(r.Context(), member)
	}

	p.writeOperation(w, p.operationReferring(nounPool, id))
}

type evictPoolRequest struct {
	Instances []string `json:"instances"`
}

// evictInstancePoolMembers removes the members a client names, and no others.
//
// The distinction from a scale down is the whole point of the call: a scale says
// *how many*, an evict says *which ones* — a client evicts the member whose
// machine is misbehaving. The closing condition says it in those terms, and the
// test asserts the neighbours survive.
//
// An id naming an instance that is not a member of this pool is refused rather
// than ignored: silently skipping it would let a client believe it evicted
// something.
func (p *Pack) evictInstancePoolMembers(w http.ResponseWriter, r *http.Request) {
	var req evictPoolRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindPool, id); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	unlock := p.lockPool(id)
	member := map[string]*resource.Resource{}
	for _, res := range p.poolMembers(id) {
		member[res.ID] = res
	}
	// Checked before anything is deleted, so a half-evicted list cannot happen:
	// the same discipline releaseIPSet applies to a batch.
	evicting := make([]*resource.Resource, 0, len(req.Instances))
	for _, want := range req.Instances {
		res, ok := member[want]
		if !ok {
			unlock()
			writeError(w, http.StatusBadRequest, "the instance is not a member of this pool")
			return
		}
		evicting = append(evicting, res)
	}
	for _, res := range evicting {
		p.env.Store.Delete(Name, kindInstance, res.ID)
	}
	// An evict lowers the pool's declared size, which is upstream's behaviour:
	// the pool does not replace what a client explicitly removed.
	_ = p.env.Store.Update(Name, kindPool, id, func(stored *resource.Resource) error {
		stored.Attrs["size"] = resource.Int64(stored, "size") - int64(len(evicting))
		return nil
	})
	unlock()

	for _, res := range evicting {
		p.destroy(r.Context(), res)
	}

	p.writeOperation(w, p.operationReferring(nounPool, id))
}

// deleteInstancePool takes its members with it.
//
// Unlike the block volume, which refuses to be deleted under its snapshots, a
// pool owns its members outright: they exist because it made them, and upstream
// destroys them with it. Leaving them behind would leave instances naming a
// manager that is gone, which is the orphan class #215 named.
func (p *Pack) deleteInstancePool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindPool, id); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	unlock := p.lockPool(id)
	members := p.poolMembers(id)
	for _, member := range members {
		p.env.Store.Delete(Name, kindInstance, member.ID)
	}
	p.env.Store.Delete(Name, kindPool, id)
	unlock()

	for _, member := range members {
		p.destroy(r.Context(), member)
	}

	p.writeOperation(w, p.operationReferring(nounPool, id))
}

// resetInstancePoolField clears one field, which is the shape upstream declares
// for this family: a DELETE on the field rather than an update carrying an empty
// value.
func (p *Pack) resetInstancePoolField(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	field := r.PathValue("field")
	// Only the fields the API describes as resettable. A path segment reaching
	// any attribute would let a client delete `size` and leave the pool
	// answering nothing where a number belongs.
	resettable := map[string]bool{
		"description": true, "labels": true, "instance-prefix": true,
		"min-available": true, "user-data": true,
	}
	if !resettable[field] {
		writeError(w, http.StatusBadRequest, "that field cannot be reset")
		return
	}
	err := p.env.Store.Update(Name, kindPool, id, func(stored *resource.Resource) error {
		switch field {
		case "labels":
			stored.Attrs["labels"] = map[string]any{}
		case "min-available":
			delete(stored.Attrs, "min-available")
		default:
			stored.Attrs[field] = ""
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounPool, id))
}

// templateByID answers whether a template exists, in the catalogue or in the
// store. A pool that accepted a template nothing offers would create members
// whose boot refuses, and the refusal would arrive at machine level rather than
// at the call the client can still fix.
func (p *Pack) templateByID(id string) (map[string]any, bool) {
	if id == "" {
		return nil, false
	}
	for _, t := range p.templates {
		if t["id"] == id {
			return t, true
		}
	}
	if res, found := p.env.Store.Get(Name, kindTemplate, id); found {
		return p.templateView(res), true
	}
	return nil, false
}

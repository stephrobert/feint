package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// What holds an Incus host that this emulator's own sweep cannot get out of,
// and the single repair that needs more than an ordinary command. The neutral
// contract is in prune.go.
//
// The measurement is #455, and the cause is three lines of the runtime's own
// schema. `networks_peers` carries three references; two cascade and one does
// not:
//
//	network_id INTEGER NOT NULL,
//	  FOREIGN KEY (network_id) REFERENCES "networks" (id) ON DELETE CASCADE
//	target_network_integration_id INTEGER ... ON DELETE CASCADE
//	target_network_id INTEGER NULL          -- no foreign key, no cascade
//
// Delete the source network and the row goes with it. Delete the *target* and
// the row survives, holding a `target_network_id` that resolves to nothing and
// a `target_network_name` that is NULL. From that moment every operation on the
// source network fails on "Failed loading target network: Network not found" —
// `peer delete`, `network unset security.acls`, `acl delete`, `network delete`.
// `incus network peer edit` cannot repair it either: it returns 0 and persists
// nothing, the target fields being immutable after creation. Verified on Incus
// 7.2, and 7.3's changelog carries nothing that touches peer rows.
//
// So the row is beyond every command the runtime offers, and the only supported
// way to reach it is `incus admin sql`, which is the runtime's own mechanism
// for exactly this and not an edit of a file behind its back.
//
// Prevention is elsewhere and it is the real fix: RemoveNetwork and Prune now
// drop the half a peering leaves on its surviving target *before* deleting a
// network, which is what manufactures the dangling row. This file is for the
// stations already carrying one.

// sqlSelect runs one read against the runtime's global database and returns the
// rows positionally, in the order the SELECT names its columns.
//
// Positional rather than by column name: two columns of the same name collapse
// in the answer's `columns` array, and a reader keyed on names would silently
// take the wrong one. Every query in this file is a constant with no
// interpolation at all, so there is nothing for a name to be smuggled through.
func (d *Incus) sqlSelect(ctx context.Context, query string) ([][]any, error) {
	out, err := d.run(ctx, "admin", "sql", "global", query, "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("read the runtime database: %w", err)
	}
	var answer struct {
		Type string  `json:"type"`
		Rows [][]any `json:"rows"`
	}
	if err := json.Unmarshal(out, &answer); err != nil {
		return nil, fmt.Errorf("decode the runtime database answer: %w", err)
	}
	return answer.Rows, nil
}

// The three reads a dangling-peer survey needs. Constants, because a query
// built from a value is the one shape this repository refuses to write.
const (
	// Every peering row, whatever project it belongs to and whoever made it.
	// Ownership is decided afterwards, against the label the emulator wrote,
	// never here: a WHERE clause on a name would be the "well-formed is not
	// authorised" mistake with a database behind it.
	peerRowsSQL = "SELECT id, network_id, name, target_network_id, " +
		"target_network_project, target_network_name, description, type FROM networks_peers"
	// The id of every network that still exists, in every project: a target
	// living in another project is a target that still resolves, so the row is
	// not dangling and is none of this code's business.
	networkIDsSQL = "SELECT id FROM networks"
	// The name and project of every network, so a row's network_id can be
	// matched against the labelled networks the API names.
	networkNamesSQL = "SELECT networks.id, projects.name, networks.name " +
		"FROM networks, projects WHERE projects.id = networks.project_id"
)

// networkRef is a network as both halves of this file can name it: the API
// answers a project and a name, the database answers an id and the same pair.
type networkRef struct {
	Project string
	Name    string
}

// peerRowDB is one row of networks_peers, and whether the emulator may touch it.
type peerRowDB struct {
	ID        int64
	NetworkID int64
	Name      string
	TargetID  int64
	HasTarget bool
	Source    networkRef
	// Ours is the answer to the only question that entitles anything here to
	// act: does the row's *source* network carry the label this emulator wrote
	// on the networks it created. It is never inferred from a name.
	Ours bool
	// Dangling says the target no longer resolves to a network.
	Dangling bool
	// Row is the whole row, so a repair can print what it removes.
	Row string
}

// networkView is one network as the API describes it, and it is read once per
// survey: this file asks three questions of the same listing, and three reads
// could answer them about three different moments of the host.
type networkView struct {
	Name    string            `json:"name"`
	Project string            `json:"project"`
	Type    string            `json:"type"`
	Config  map[string]string `json:"config"`
}

func (d *Incus) networkViews(ctx context.Context) ([]networkView, error) {
	out, err := d.run(ctx, "query", "/1.0/networks?recursion=1")
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	var items []networkView
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("decode the network list: %w", err)
	}
	return items, nil
}

// labelledNetworkRefs answers which networks carry the emulator's own label,
// read through the API rather than the database: it is the same question
// mustOwn asks, against the same mark EnsureNetwork writes, and reading it
// where the driver already reads it keeps one answer rather than two.
func labelledNetworkRefs(views []networkView) map[networkRef]bool {
	ours := map[networkRef]bool{}
	for _, item := range views {
		if item.Config["user."+LabelKey] == "" {
			continue
		}
		project := item.Project
		if project == "" {
			project = "default"
		}
		ours[networkRef{Project: project, Name: item.Name}] = true
	}
	return ours
}

// peerRows reads every peering row and decides two things about each: whether
// its target still resolves, and whether its source network is one the emulator
// labelled.
//
// The second is the authorisation, and it is deliberately computed here, once,
// rather than at each use: TestForceLeavesAThirdPartysDanglingPeerAlone plants
// a dangling row on an unlabelled network and fails the moment this stops
// discriminating.
func (d *Incus) peerRows(ctx context.Context, views []networkView) ([]peerRowDB, error) {
	rows, err := d.sqlSelect(ctx, peerRowsSQL)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	idRows, err := d.sqlSelect(ctx, networkIDsSQL)
	if err != nil {
		return nil, err
	}
	exists := make(map[int64]bool, len(idRows))
	for _, row := range idRows {
		if id, ok := sqlInt(row, 0); ok {
			exists[id] = true
		}
	}
	nameRows, err := d.sqlSelect(ctx, networkNamesSQL)
	if err != nil {
		return nil, err
	}
	named := make(map[int64]networkRef, len(nameRows))
	for _, row := range nameRows {
		id, ok := sqlInt(row, 0)
		if !ok {
			continue
		}
		named[id] = networkRef{Project: sqlText(row, 1), Name: sqlText(row, 2)}
	}
	ours := labelledNetworkRefs(views)

	out := make([]peerRowDB, 0, len(rows))
	for _, row := range rows {
		id, ok := sqlInt(row, 0)
		if !ok {
			continue
		}
		networkID, ok := sqlInt(row, 1)
		if !ok {
			continue
		}
		target, hasTarget := sqlInt(row, 3)
		source := named[networkID]
		encoded, err := json.Marshal(map[string]any{
			"id": id, "network_id": networkID, "name": sqlText(row, 2),
			"target_network_id": sqlAny(row, 3), "target_network_project": sqlAny(row, 4),
			"target_network_name": sqlAny(row, 5), "description": sqlAny(row, 6),
			"type": sqlAny(row, 7),
		})
		if err != nil {
			return nil, fmt.Errorf("encode peering row %d: %w", id, err)
		}
		out = append(out, peerRowDB{
			ID:        id,
			NetworkID: networkID,
			Name:      sqlText(row, 2),
			TargetID:  target,
			HasTarget: hasTarget,
			Source:    source,
			Ours:      ours[source],
			Dangling:  hasTarget && !exists[target],
			Row:       string(encoded),
		})
	}
	return out, nil
}

// sqlInt reads one column as an integer. A JSON number arrives as a float64, a
// NULL as nil, and the second return distinguishes "there is no value" from
// "the value is zero" — the difference between a peering that names no target
// and one that names network 0.
func sqlInt(row []any, at int) (int64, bool) {
	if at >= len(row) {
		return 0, false
	}
	number, ok := row[at].(float64)
	if !ok {
		return 0, false
	}
	return int64(number), true
}

func sqlText(row []any, at int) string {
	if at >= len(row) {
		return ""
	}
	text, ok := row[at].(string)
	if !ok {
		return ""
	}
	return text
}

// sqlAny reads one column verbatim, for the copy of the row a repair prints so
// an operator can put it back. Out of range gives nil, which encodes as null.
//
// Bounded for the same reason sqlInt and sqlText are, and it was not: this row
// is the answer of another process, decoded from JSON. A shorter answer than
// the SELECT names — another Incus, a table this one does not have — indexed a
// slice past its end and panicked, in the one command an operator runs to get
// a station out of trouble. Nothing else in this file trusts the length, and
// the repair path is the last place that should.
// TestAShortDatabaseAnswerIsInertRatherThanFatal fails without it.
func sqlAny(row []any, at int) any {
	if at >= len(row) {
		return nil
	}
	return row[at]
}

// Traps implements Repairer.
//
// Three states, and each of them is one no healthy run ever produces. That
// property is the whole design of this check and is worth stating, because the
// obvious fourth candidate fails it: a rule set attached to a network is the
// *normal* state of every isolated OVN network — IsolateNetwork ends by running
// `network set <network> security.acls <name>` on every network with a
// neighbour to keep out. Reporting that as stuck would fail every healthy run,
// which is exactly how #426's doorstep learned to fire on hosts nothing was
// going to fail on. So the rule set is reported only when the network holding
// it is itself trapped, which is when the detach is genuinely refused.
func (d *Incus) Traps(ctx context.Context) ([]Trap, error) {
	var traps []Trap

	views, err := d.networkViews(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := d.peerRows(ctx, views)
	if err != nil {
		return nil, err
	}
	trapped := map[string]bool{}
	for _, row := range rows {
		if !row.Dangling || !row.Ours {
			continue
		}
		trapped[row.Source.Name] = true
		traps = append(traps, Trap{
			Kind: TrapDanglingPeer,
			Name: row.Source.Name + "/" + row.Name,
			Why: fmt.Sprintf("its peering names network %d, which no longer exists, so every operation on %s "+
				"fails loading a target that resolves to nothing", row.TargetID, row.Source.Name),
			Repairable: true,
			Row:        row.Row,
		})
	}

	stripped, err := d.strippedDelegations(ctx, views)
	if err != nil {
		return nil, err
	}
	for _, network := range stripped {
		trapped[network.Name] = true
		traps = append(traps, Trap{
			Kind: TrapStrippedUplink,
			Name: network.Name,
			Why: fmt.Sprintf("its block %s is no longer delegated to the uplink %s, so the runtime refuses "+
				"every update of it, the detach that would free it included", network.Block, d.uplinkName()),
		})
	}

	traps = append(traps, ruleSetsHeldBy(views, trapped)...)

	sort.Slice(traps, func(i, j int) bool {
		if traps[i].Kind != traps[j].Kind {
			return traps[i].Kind < traps[j].Kind
		}
		return traps[i].Name < traps[j].Name
	})
	return traps, nil
}

// strippedNetwork is one of the emulator's OVN networks whose block the uplink
// no longer carries.
type strippedNetwork struct {
	Name  string
	Block string
}

// strippedDelegations names the labelled OVN networks attached to the uplink
// whose block is absent from its ipv4.routes.
//
// It is not gated on d.OVN, and that is deliberate: the issue's own
// reproduction sweeps the trapped host with `feint clean --vm incus`. Whether
// the uplink is stripped is a fact about the host, not about the mode this
// process happens to run in, and a check that answered "nothing" because of the
// flag it was given would be the instrument lying.
func (d *Incus) strippedDelegations(ctx context.Context, views []networkView) ([]strippedNetwork, error) {
	networks := d.ovnNetworksOnUplink(views)
	if len(networks) == 0 {
		return nil, nil
	}
	out, err := d.run(ctx, "network", "get", d.uplinkName(), "ipv4.routes")
	if err != nil {
		if isNotFound(err) {
			// No uplink, and networks that name it: they are already beyond
			// repair by any route edit, and the peering half below is what will
			// name them. Nothing to report here rather than a route list of one
			// object that does not exist.
			return nil, nil
		}
		return nil, fmt.Errorf("read routes of uplink %s: %w", d.uplinkName(), err)
	}
	routes := strings.TrimSpace(string(out))
	var stripped []strippedNetwork
	for _, network := range networks {
		if !routeListContains(routes, network.Block) {
			stripped = append(stripped, network)
		}
	}
	return stripped, nil
}

// ovnNetworksOnUplink lists the labelled OVN networks drawing from the uplink,
// with the block each one carries. It is liveDelegations' reading with the
// names kept: the adopt path only needs the blocks, a report needs to say which
// network each belongs to.
func (d *Incus) ovnNetworksOnUplink(views []networkView) []strippedNetwork {
	var networks []strippedNetwork
	for _, item := range views {
		if item.Type != "ovn" || item.Config["user."+LabelKey] == "" ||
			item.Config["network"] != d.uplinkName() {
			continue
		}
		block, err := maskedBlock(item.Config["ipv4.address"])
		if err != nil {
			continue
		}
		networks = append(networks, strippedNetwork{Name: item.Name, Block: block})
	}
	return networks
}

// ruleSetsHeldBy names the emulator's own rule sets attached to a network that
// is itself trapped. See Traps for why the bare "attached" form is not a trap.
//
// There is deliberately no early return on an empty trapped set, tempting as it
// is: the loop below would then be skipped on exactly the healthy host this
// distinction exists for, and the condition inside it — the only thing keeping
// a normal attachment from being reported — would never be exercised. A guard a
// fast path walks around is a guard no falsification can reach, which is what
// the first replay of this spec measured: the mutation that turns every
// attached rule set into a trap left TestTrapsAreSilentOnAHealthyOVNRun green.
func ruleSetsHeldBy(views []networkView, trapped map[string]bool) []Trap {
	var traps []Trap
	for _, item := range views {
		if !trapped[item.Name] || item.Config["user."+LabelKey] == "" {
			continue
		}
		for _, name := range strings.Split(item.Config["security.acls"], ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			traps = append(traps, Trap{
				Kind: TrapHeldFirewall,
				Name: name,
				Why: fmt.Sprintf("it is attached to %s, which is trapped: the rule set is in use by the network, "+
					"the network is in use by the rule set, and the detach that breaks the cycle is refused",
					item.Name),
			})
		}
	}
	return traps
}

// Repair implements Repairer: it removes the peering rows whose target resolves
// to no network, and nothing else.
//
// The scope is the point. A row is removed only when its *source* network
// carries the label this emulator wrote — the same question mustOwn asks of a
// network before the driver touches it, asked of a database row. Never a name
// pattern: a `--force` able to reach a third party's peering row would be a
// worse defect than the one it repairs, and "fnt-" is a prefix anybody may type.
//
// TestForceLeavesAThirdPartysDanglingPeerAlone fails without the Ours filter,
// and TestForceRemovesADanglingPeerOfOurOwn fails if it refuses everything.
func (d *Incus) Repair(ctx context.Context) ([]Trap, error) {
	// Re-read rather than act on what Traps returned: the caller printed that
	// list, a human read it, and neither the reading nor the printing is a
	// permission. Ownership is derived again here, from the host as it is now.
	views, err := d.networkViews(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := d.peerRows(ctx, views)
	if err != nil {
		return nil, err
	}
	var cleared []Trap
	var failures []string
	for _, row := range rows {
		if !row.Dangling || !row.Ours {
			continue
		}
		// The id is an integer decoded from the runtime's own answer, so the
		// statement carries no text from anywhere. Nothing else in this file
		// builds SQL from a value, and nothing else should.
		if _, err := d.run(ctx, "admin", "sql", "global",
			fmt.Sprintf("DELETE FROM networks_peers WHERE id=%d", row.ID)); err != nil {
			failures = append(failures, fmt.Sprintf("peering row %d of %s: %v", row.ID, row.Source.Name, err))
			continue
		}
		cleared = append(cleared, Trap{
			Kind:       TrapDanglingPeer,
			Name:       row.Source.Name + "/" + row.Name,
			Why:        fmt.Sprintf("its target network %d no longer exists", row.TargetID),
			Repairable: true,
			Row:        row.Row,
		})
	}
	if len(failures) > 0 {
		return cleared, fmt.Errorf("could not clear %d peering row(s): %s",
			len(failures), strings.Join(failures, "; "))
	}
	return cleared, nil
}

// Package exoscale emulates the Exoscale API v2.
//
// Exoscale is the third dialect: plain REST on /v2/<resource>, but mutations
// return an Operation object that the client polls until it reaches "success".
// Terraform and egoscale both depend on that indirection, so the pack models it
// even though the emulator applies changes synchronously.
//
// Authentication (EXO2-HMAC-SHA256) is accepted without verification. Clients
// reach the emulator through EXOSCALE_API_ENDPOINT, which the Terraform provider
// reads directly.
//
// This pack is an amorce: compute instances only.
package exoscale

import (
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/cloudinit"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/serialise"
)

// Name is the provider key.
const Name = "exoscale"

const (
	kindInstance  = "compute-instance"
	kindOperation = "operation"

	// nounInstance is the resource segment operations refer to; the kind above
	// is the store's key and predates it, kept so a snapshot taken before the
	// lifecycle work still restores.
	nounInstance = "instance"

	runningState = "running"
	stoppedState = "stopped"

	// Internal attributes, never emitted by a view.
	attrProtected            = "feint-protected"
	attrSecurityGroupIDs     = "feint-security-group-ids"
	attrAntiAffinityGroupIDs = "feint-anti-affinity-group-ids"
	attrElasticIPIDs         = "feint-elastic-ip-ids"
)

// Pack implements emulator.Pack for Exoscale.
type Pack struct {
	env *emulator.Env
}

// lockAddresses serializes elastic IP and lease allocation, which is
// read-modify-write over the store: freeElasticAddress rebuilds the used set
// from what exists, answers the lowest free address, and the caller then
// stores it. Two requests interleaving there receive the same address.
//
// Not a precaution. The barrage of #134 found it on its first run — three
// addresses handed to two elastic IPs each, out of sixteen creates — and it
// was the same defect the Scaleway pack fixed for its own pools long ago and
// this one never received. CLAUDE.md names that shape: written twice, fixed
// once, alive in the other copy — which is why the lock now lives in
// core/serialise, shared with every pack, instead of being a mutex each one
// remembers to grow.
//
// TestAnExoscaleBarrageLeavesTheStoreCoherent fails without this.
func (p *Pack) lockAddresses() func() { return serialise.Lock(Name + "/addresses") }

// New returns an Exoscale pack backed by env.
func New(env *emulator.Env) *Pack { return &Pack{env: env} }

// Name implements emulator.Pack.
func (p *Pack) Name() string { return Name }

// Operation names are the operationId Exoscale's own OpenAPI document gives,
// kebab-case and untranslated. Their Go SDK renames them to Go conventions
// (list-instances becomes ListInstances), and this pack used those names until a
// scan of the document showed all eight of them matched nothing: picking the
// renamed form means performing a translation nobody can check, which is exactly
// how a hand-written name goes wrong.
//
// The path prefix is /v2, which the document declares in its server URL, so a
// route's path is the prefix and the operation's own path.
func operation(id string) string { return "exoscale/v2." + id }

// Routes implements emulator.Pack.
func (p *Pack) Routes() []emulator.Route {
	return p.guardSplitClients([]emulator.Route{
		{Method: "GET", Path: "/v2/instance", Operation: operation("list-instances"), Handler: p.listInstances},
		{Method: "POST", Path: "/v2/instance", Operation: operation("create-instance"), Handler: p.createInstance},
		{Method: "GET", Path: "/v2/instance/{id}", Operation: operation("get-instance"), Handler: p.getInstance},
		{Method: "DELETE", Path: "/v2/instance/{id}", Operation: operation("delete-instance"), Handler: p.deleteInstance},
		// The polling read of this pack's own async model, and the one operation
		// that is undriven *because* the emulator is fast (#174): every
		// operation this pack returns is already in its terminal state, so a
		// client with nothing to wait for never asks again. It stays mounted
		// because the day an operation is not instantaneous — a machine
		// runtime taking seconds to start a container — a client that polls
		// must find it served rather than 404.
		{Method: "GET", Path: "/v2/operation/{id}", Operation: operation("get-operation"), Handler: p.getOperation,
			Undriven: "every operation this pack answers is already terminal, so no client has anything to poll for; it stays served for the client that polls anyway"},

		// The lifecycle, batch 2 of docs/roadmap-exoscale-iaas.md. The verbs live
		// in the same path segment as the identifier — PUT /instance/{id}:stop —
		// which is the action-suffix form the route table resolves itself.
		{Method: "PUT", Path: "/v2/instance/{id}", Operation: operation("update-instance"), Handler: p.updateInstance},
		{Method: "PUT", Path: "/v2/instance/{id}:start", Operation: operation("start-instance"), Handler: p.startInstance},
		{Method: "PUT", Path: "/v2/instance/{id}:stop", Operation: operation("stop-instance"), Handler: p.stopInstance},
		{Method: "PUT", Path: "/v2/instance/{id}:reboot", Operation: operation("reboot-instance"), Handler: p.rebootInstance},
		{Method: "PUT", Path: "/v2/instance/{id}:reset", Operation: operation("reset-instance"), Handler: p.resetInstance},
		{Method: "PUT", Path: "/v2/instance/{id}:scale", Operation: operation("scale-instance"), Handler: p.scaleInstance},
		{Method: "PUT", Path: "/v2/instance/{id}:resize-disk", Operation: operation("resize-instance-disk"), Handler: p.resizeInstanceDisk},
		{Method: "PUT", Path: "/v2/instance/{id}:add-protection", Operation: operation("add-instance-protection"), Handler: p.addInstanceProtection},
		{Method: "PUT", Path: "/v2/instance/{id}:remove-protection", Operation: operation("remove-instance-protection"), Handler: p.removeInstanceProtection},

		// Security groups and their rules.
		{Method: "POST", Path: "/v2/security-group", Operation: operation("create-security-group"), Handler: p.createSecurityGroup},
		{Method: "GET", Path: "/v2/security-group", Operation: operation("list-security-groups"), Handler: p.listSecurityGroups},
		// `exo compute security-group show` reads the list and filters it in the
		// client, because a user names a group and the API keys it by id. The
		// same holds for the elastic IP and the snapshot below: three reads
		// served, and three the CLI has no reason to make (measured, #174).
		{Method: "GET", Path: "/v2/security-group/{id}", Operation: operation("get-security-group"), Handler: p.getSecurityGroup,
			Undriven: "`exo compute security-group show` resolves a group by name, which it does by listing and filtering in the client, so the per-id read is never called"},
		{Method: "DELETE", Path: "/v2/security-group/{id}", Operation: operation("delete-security-group"), Handler: p.deleteSecurityGroup},
		{Method: "POST", Path: "/v2/security-group/{id}/rules", Operation: operation("add-rule-to-security-group"), Handler: p.addRuleToSecurityGroup},
		{Method: "DELETE", Path: "/v2/security-group/{id}/rules/{rule}", Operation: operation("delete-rule-from-security-group"), Handler: p.deleteRuleFromSecurityGroup},
		{Method: "PUT", Path: "/v2/security-group/{id}:attach", Operation: operation("attach-instance-to-security-group"), Handler: p.attachInstanceToSecurityGroup},
		{Method: "PUT", Path: "/v2/security-group/{id}:detach", Operation: operation("detach-instance-from-security-group"), Handler: p.detachInstanceFromSecurityGroup},

		// Anti-affinity groups.
		{Method: "POST", Path: "/v2/anti-affinity-group", Operation: operation("create-anti-affinity-group"), Handler: p.createAntiAffinityGroup},
		{Method: "GET", Path: "/v2/anti-affinity-group", Operation: operation("list-anti-affinity-groups"), Handler: p.listAntiAffinityGroups},
		{Method: "GET", Path: "/v2/anti-affinity-group/{id}", Operation: operation("get-anti-affinity-group"), Handler: p.getAntiAffinityGroup},
		{Method: "DELETE", Path: "/v2/anti-affinity-group/{id}", Operation: operation("delete-anti-affinity-group"), Handler: p.deleteAntiAffinityGroup},

		// Elastic IPs, attachment included.
		{Method: "POST", Path: "/v2/elastic-ip", Operation: operation("create-elastic-ip"), Handler: p.createElasticIP},
		{Method: "GET", Path: "/v2/elastic-ip", Operation: operation("list-elastic-ips"), Handler: p.listElasticIPs},
		{Method: "GET", Path: "/v2/elastic-ip/{id}", Operation: operation("get-elastic-ip"), Handler: p.getElasticIP,
			Undriven: "`exo compute elastic-ip show` takes the address a user reads off a list, so it filters the list it already has rather than reading by id"},
		{Method: "PUT", Path: "/v2/elastic-ip/{id}", Operation: operation("update-elastic-ip"), Handler: p.updateElasticIP},
		{Method: "DELETE", Path: "/v2/elastic-ip/{id}", Operation: operation("delete-elastic-ip"), Handler: p.deleteElasticIP},
		{Method: "PUT", Path: "/v2/elastic-ip/{id}:attach", Operation: operation("attach-instance-to-elastic-ip"), Handler: p.attachInstanceToElasticIP},
		{Method: "PUT", Path: "/v2/elastic-ip/{id}:detach", Operation: operation("detach-instance-from-elastic-ip"), Handler: p.detachInstanceFromElasticIP},

		// Private networks and their leases, batch 3 (#9). The action-suffix
		// routes resolve like the instance's own; the reset route is the one
		// field their API description declares resettable.
		{Method: "POST", Path: "/v2/private-network", Operation: operation("create-private-network"), Handler: p.createPrivateNetwork},
		{Method: "GET", Path: "/v2/private-network", Operation: operation("list-private-networks"), Handler: p.listPrivateNetworks},
		{Method: "GET", Path: "/v2/private-network/{id}", Operation: operation("get-private-network"), Handler: p.getPrivateNetwork},
		{Method: "PUT", Path: "/v2/private-network/{id}", Operation: operation("update-private-network"), Handler: p.updatePrivateNetwork},
		{Method: "DELETE", Path: "/v2/private-network/{id}", Operation: operation("delete-private-network"), Handler: p.deletePrivateNetwork},
		// The three field resets share one reason, measured on all three: the CLI
		// clears a field by sending the update with an empty value, never by
		// deleting the field. `exo compute elastic-ip update --description ""`
		// issues a PUT and no DELETE, and so does the private network one.
		{Method: "DELETE", Path: "/v2/private-network/{id}/{field}", Operation: operation("reset-private-network-field"), Handler: p.resetPrivateNetworkField,
			Undriven: "the CLI clears a field by sending the update with an empty value, so the per-field DELETE the API declares is never issued"},
		{Method: "PUT", Path: "/v2/private-network/{id}:attach", Operation: operation("attach-instance-to-private-network"), Handler: p.attachInstanceToPrivateNetwork},
		{Method: "PUT", Path: "/v2/private-network/{id}:detach", Operation: operation("detach-instance-from-private-network"), Handler: p.detachInstanceFromPrivateNetwork},
		{Method: "PUT", Path: "/v2/private-network/{id}:update-ip", Operation: operation("update-private-network-instance-ip"), Handler: p.updatePrivateNetworkInstanceIP},

		// The external sources of a security group, the last piece of batch 3.
		{Method: "PUT", Path: "/v2/security-group/{id}:add-source", Operation: operation("add-external-source-to-security-group"), Handler: p.addExternalSourceToSecurityGroup},
		{Method: "PUT", Path: "/v2/security-group/{id}:remove-source", Operation: operation("remove-external-source-from-security-group"), Handler: p.removeExternalSourceFromSecurityGroup},

		// Deploy targets: a read the CLI makes while resolving a create. An
		// emulated account has none, and an empty list is the honest inventory.
		{Method: "GET", Path: "/v2/deploy-target", Operation: operation("list-deploy-targets"), Handler: p.listDeployTargets},
		{Method: "GET", Path: "/v2/deploy-target/{id}", Operation: operation("get-deploy-target"), Handler: p.getDeployTarget,
			// The list is driven and answers empty, which is the honest
			// inventory of an emulated account: deploy targets are dedicated
			// hardware Exoscale assigns, not something a client creates. With
			// nothing in the list there is no id to read, and seeding a fake one
			// would be inventing inventory — the thing docs/limits.md refuses.
			Undriven: "the list this pack serves is empty, because an emulated account owns no dedicated hardware, so no client ever holds an id to read"},

		// The first call the official CLI makes, and the address every call
		// after it uses. Measured, not assumed: see catalog.go.
		{Method: "GET", Path: "/v2/zone", Operation: operation("list-zones"), Handler: p.listZones},
		{Method: "GET", Path: "/v2/template", Operation: operation("list-templates"), Handler: p.listTemplates},
		{Method: "GET", Path: "/v2/template/{id}", Operation: operation("get-template"), Handler: p.getTemplate},
		// Snapshots and the templates they promote into (#173). Twenty-four
		// operations belonged to no open batch, which is the state Declined()
		// refuses to hold: "not triaged yet" and "out of scope" are not the same
		// answer, and only one of them may be silent.
		{Method: "POST", Path: "/v2/instance/{id}:create-snapshot", Operation: operation("create-snapshot"), Handler: p.createSnapshot},
		{Method: "GET", Path: "/v2/snapshot", Operation: operation("list-snapshots"), Handler: p.listSnapshots},
		{Method: "GET", Path: "/v2/snapshot/{id}", Operation: operation("get-snapshot"), Handler: p.getSnapshot,
			Undriven: "`exo compute instance snapshot show` lists the snapshots and picks the one it wants in the client, so the per-id read is never called"},
		{Method: "DELETE", Path: "/v2/snapshot/{id}", Operation: operation("delete-snapshot"), Handler: p.deleteSnapshot},
		{Method: "POST", Path: "/v2/instance/{id}:revert-snapshot", Operation: operation("revert-instance-to-snapshot"), Handler: p.revertInstanceToSnapshot},
		// The CLI has a flag for this — `instance-template register
		// --from-snapshot` — and it does not use this route: measured, it calls
		// export-snapshot first to obtain a pre-signed URL, then registers a
		// template from that URL. export-snapshot is declined here (it hands
		// back an object-storage address this emulator does not serve), so the
		// whole path stops before anything could reach :promote.
		{Method: "POST", Path: "/v2/snapshot/{id}:promote", Operation: operation("promote-snapshot-to-template"), Handler: p.promoteSnapshotToTemplate,
			Undriven: "the CLI's --from-snapshot promotes through export-snapshot and a URL, which this pack declines, so it never issues the promote call the SDK declares"},
		{Method: "POST", Path: "/v2/template", Operation: operation("register-template"), Handler: p.registerTemplate},
		{Method: "POST", Path: "/v2/template/{id}", Operation: operation("copy-template"), Handler: p.copyTemplate,
			Undriven: "copying a template targets another zone, and this emulator serves exactly one, so the CLI has nothing to copy to and no subcommand that would ask"},
		{Method: "PUT", Path: "/v2/template/{id}", Operation: operation("update-template"), Handler: p.updateTemplate,
			Undriven: "`exo compute instance-template` offers register, list, show and delete, and no update, so a client cannot rename a template it owns"},
		{Method: "DELETE", Path: "/v2/template/{id}", Operation: operation("delete-template"), Handler: p.deleteTemplate},
		// The account-level reads and the two field resets, same triage (#173).
		{Method: "GET", Path: "/v2/organization", Operation: operation("get-organization"), Handler: p.getOrganization},
		{Method: "GET", Path: "/v2/event", Operation: operation("list-events"), Handler: p.listEvents},
		{Method: "POST", Path: "/v2/instance/{id}:enable-tpm", Operation: operation("enable-tpm"), Handler: p.enableTPM},
		{Method: "DELETE", Path: "/v2/instance/{id}/{field}", Operation: operation("reset-instance-field"), Handler: p.resetInstanceField,
			Undriven: "the CLI clears a field by sending the update with an empty value, so the per-field DELETE the API declares is never issued"},
		{Method: "DELETE", Path: "/v2/elastic-ip/{id}/{field}", Operation: operation("reset-elastic-ip-field"), Handler: p.resetElasticIPField,
			Undriven: "the CLI clears a field by sending the update with an empty value, so the per-field DELETE the API declares is never issued"},
		{Method: "GET", Path: "/v2/instance-type", Operation: operation("list-instance-types"), Handler: p.listInstanceTypes},
		{Method: "GET", Path: "/v2/instance-type/{id}", Operation: operation("get-instance-type"), Handler: p.getInstanceType},

		// Block Storage (EXO-4, #12). A product a client declares in an ordinary
		// configuration, and the thirteen operations are the whole of it: the
		// volume's CRUD, its attachment to one instance, its resize, and the
		// snapshots taken from it. block.go says what is stored, what is
		// computed, and which refusals are upstream's.
		{Method: "POST", Path: "/v2/block-storage", Operation: operation("create-block-storage-volume"), Handler: p.createBlockVolume},
		{Method: "GET", Path: "/v2/block-storage", Operation: operation("list-block-storage-volumes"), Handler: p.listBlockVolumes},
		{Method: "GET", Path: "/v2/block-storage/{id}", Operation: operation("get-block-storage-volume"), Handler: p.getBlockVolume},
		{Method: "PUT", Path: "/v2/block-storage/{id}", Operation: operation("update-block-storage-volume"), Handler: p.updateBlockVolume},
		{Method: "DELETE", Path: "/v2/block-storage/{id}", Operation: operation("delete-block-storage-volume"), Handler: p.deleteBlockVolume},
		{Method: "PUT", Path: "/v2/block-storage/{id}:attach", Operation: operation("attach-block-storage-volume-to-instance"), Handler: p.attachBlockVolumeToInstance},
		{Method: "PUT", Path: "/v2/block-storage/{id}:detach", Operation: operation("detach-block-storage-volume"), Handler: p.detachBlockVolume},
		{Method: "PUT", Path: "/v2/block-storage/{id}:resize-volume", Operation: operation("resize-block-storage-volume"), Handler: p.resizeBlockVolume},
		{Method: "POST", Path: "/v2/block-storage/{id}:create-snapshot", Operation: operation("create-block-storage-snapshot"), Handler: p.createBlockSnapshot},
		{Method: "GET", Path: "/v2/block-storage-snapshot", Operation: operation("list-block-storage-snapshots"), Handler: p.listBlockSnapshots},
		{Method: "GET", Path: "/v2/block-storage-snapshot/{id}", Operation: operation("get-block-storage-snapshot"), Handler: p.getBlockSnapshot,
			// The fourth per-id read this CLI never makes, and the pattern is
			// now established rather than suspected: `exo` resolves a security
			// group, an elastic IP, an instance snapshot and this one by listing
			// and filtering in the client, because a user names a resource and
			// the API keys it by id. Measured on each (#174).
			//
			// The Terraform provider *does* call it — measured against the
			// patched fork docs/limits.md pins, where an apply of
			// exoscale_block_storage_volume_snapshot reads it back by id. That
			// is why the route is served rather than declined, and it is not why
			// it would be driven: a client this project patched is not the
			// official client, and that section says so.
			Undriven: "`exo compute block-storage snapshot show` lists the snapshots and picks its one in the client, so the per-id read has no caller among the published clients"},
		{Method: "PUT", Path: "/v2/block-storage-snapshot/{id}", Operation: operation("update-block-storage-snapshot"), Handler: p.updateBlockSnapshot},
		{Method: "DELETE", Path: "/v2/block-storage-snapshot/{id}", Operation: operation("delete-block-storage-snapshot"), Handler: p.deleteBlockSnapshot},

		// SSH keys. On the critical path of a create: the official CLI
		// registers one of its own before it posts the instance.
		{Method: "POST", Path: "/v2/ssh-key", Operation: operation("register-ssh-key"), Handler: p.registerSSHKey},
		{Method: "GET", Path: "/v2/ssh-key", Operation: operation("list-ssh-keys"), Handler: p.listSSHKeys},

		// Quotas, which `exo limits` reads. See catalog.go for why they are
		// counted rather than invented.
		{Method: "GET", Path: "/v2/quota", Operation: operation("list-quotas"), Handler: p.listQuotas},
		{Method: "GET", Path: "/v2/quota/{name}", Operation: operation("get-quota"), Handler: p.getQuota,
			Undriven: "`exo limits` reads the whole quota list and prints it, so the per-name read has no client path even though the SDK declares one"},
		{Method: "GET", Path: "/v2/ssh-key/{name}", Operation: operation("get-ssh-key"), Handler: p.getSSHKey},
		{Method: "DELETE", Path: "/v2/ssh-key/{name}", Operation: operation("delete-ssh-key"), Handler: p.deleteSSHKey},
	})
}

// Declined implements emulator.Pack.
//
// The names were wrong here too, and for the same reason. They are checked now:
// the drift report calls out an entry matching nothing upstream.
func (p *Pack) Declined() []emulator.Decline {
	// Exoscale's managed services, declined by name.
	//
	// Listed one by one rather than by prefix, deliberately: a prefix rule would
	// swallow whatever upstream adds under a declined family, and the whole point
	// of this file is that additions are seen. The scan reports a new operation
	// as untriaged even inside a family already refused, and somebody decides.
	//
	// What is NOT here is the IaaS surface — instances, block storage, private
	// networks, elastic IPs, security groups, load balancers, snapshots,
	// templates, anti-affinity groups, VPCs. Those are the work list this pack is
	// built to serve, and declining them to flatten the report would be the
	// widened denominator docs/roadmap.md forbids.
	return slices.Concat(
		// Managed databases: PostgreSQL, MySQL, Kafka, OpenSearch, Valkey,
		// ClickHouse, Grafana, their users, their settings, their integrations
		// and their migrations.
		//
		// The largest family upstream by a wide margin, and the one an emulator
		// can say least about. Each service is a real engine Exoscale runs,
		// backs up and upgrades; the emulator has no engine, and a create that
		// answered "running" would hand a client a connection string to nothing.
		// The distance between the two is not a matter of routes to write.
		emulator.Because("these are real database engines Exoscale runs and backs up, and a create answering that it is running would hand a client a connection string to nothing",
			"exoscale/v2.attach-dbaas-service-to-endpoint",
			"exoscale/v2.create-dbaas-clickhouse-user",
			"exoscale/v2.create-dbaas-external-endpoint-datadog",
			"exoscale/v2.create-dbaas-external-endpoint-elasticsearch",
			"exoscale/v2.create-dbaas-external-endpoint-opensearch",
			"exoscale/v2.create-dbaas-external-endpoint-prometheus",
			"exoscale/v2.create-dbaas-external-endpoint-rsyslog",
			"exoscale/v2.create-dbaas-integration",
			"exoscale/v2.create-dbaas-kafka-schema-registry-acl-config",
			"exoscale/v2.create-dbaas-kafka-topic-acl-config",
			"exoscale/v2.create-dbaas-kafka-user",
			"exoscale/v2.create-dbaas-mysql-database",
			"exoscale/v2.create-dbaas-mysql-user",
			"exoscale/v2.create-dbaas-opensearch-user",
			"exoscale/v2.create-dbaas-pg-connection-pool",
			"exoscale/v2.create-dbaas-pg-database",
			"exoscale/v2.create-dbaas-pg-upgrade-check",
			"exoscale/v2.create-dbaas-postgres-user",
			"exoscale/v2.create-dbaas-service-clickhouse",
			"exoscale/v2.create-dbaas-service-grafana",
			"exoscale/v2.create-dbaas-service-kafka",
			"exoscale/v2.create-dbaas-service-mysql",
			"exoscale/v2.create-dbaas-service-opensearch",
			"exoscale/v2.create-dbaas-service-pg",
			"exoscale/v2.create-dbaas-service-thanos",
			"exoscale/v2.create-dbaas-service-valkey",
			"exoscale/v2.create-dbaas-task-migration-check",
			"exoscale/v2.create-dbaas-valkey-user",
			"exoscale/v2.delete-dbaas-clickhouse-role",
			"exoscale/v2.delete-dbaas-clickhouse-user",
			"exoscale/v2.delete-dbaas-external-endpoint-datadog",
			"exoscale/v2.delete-dbaas-external-endpoint-elasticsearch",
			"exoscale/v2.delete-dbaas-external-endpoint-opensearch",
			"exoscale/v2.delete-dbaas-external-endpoint-prometheus",
			"exoscale/v2.delete-dbaas-external-endpoint-rsyslog",
			"exoscale/v2.delete-dbaas-integration",
			"exoscale/v2.delete-dbaas-kafka-schema-registry-acl-config",
			"exoscale/v2.delete-dbaas-kafka-topic-acl-config",
			"exoscale/v2.delete-dbaas-kafka-user",
			"exoscale/v2.delete-dbaas-mysql-database",
			"exoscale/v2.delete-dbaas-mysql-user",
			"exoscale/v2.delete-dbaas-opensearch-user",
			"exoscale/v2.delete-dbaas-pg-connection-pool",
			"exoscale/v2.delete-dbaas-pg-database",
			"exoscale/v2.delete-dbaas-postgres-user",
			"exoscale/v2.delete-dbaas-service",
			"exoscale/v2.delete-dbaas-service-clickhouse",
			"exoscale/v2.delete-dbaas-service-grafana",
			"exoscale/v2.delete-dbaas-service-kafka",
			"exoscale/v2.delete-dbaas-service-mysql",
			"exoscale/v2.delete-dbaas-service-opensearch",
			"exoscale/v2.delete-dbaas-service-pg",
			"exoscale/v2.delete-dbaas-service-thanos",
			"exoscale/v2.delete-dbaas-service-valkey",
			"exoscale/v2.delete-dbaas-valkey-user",
			"exoscale/v2.detach-dbaas-service-from-endpoint",
			"exoscale/v2.enable-dbaas-mysql-writes",
			"exoscale/v2.get-dbaas-ca-certificate",
			"exoscale/v2.get-dbaas-clickhouse-acl-config",
			"exoscale/v2.get-dbaas-external-endpoint-datadog",
			"exoscale/v2.get-dbaas-external-endpoint-elasticsearch",
			"exoscale/v2.get-dbaas-external-endpoint-opensearch",
			"exoscale/v2.get-dbaas-external-endpoint-prometheus",
			"exoscale/v2.get-dbaas-external-endpoint-rsyslog",
			"exoscale/v2.get-dbaas-external-integration",
			"exoscale/v2.get-dbaas-external-integration-settings-datadog",
			"exoscale/v2.get-dbaas-integration",
			"exoscale/v2.get-dbaas-kafka-acl-config",
			"exoscale/v2.get-dbaas-migration-status",
			"exoscale/v2.get-dbaas-opensearch-acl-config",
			"exoscale/v2.get-dbaas-service-clickhouse",
			"exoscale/v2.get-dbaas-service-grafana",
			"exoscale/v2.get-dbaas-service-kafka",
			"exoscale/v2.get-dbaas-service-logs",
			"exoscale/v2.get-dbaas-service-metrics",
			"exoscale/v2.get-dbaas-service-mysql",
			"exoscale/v2.get-dbaas-service-opensearch",
			"exoscale/v2.get-dbaas-service-pg",
			"exoscale/v2.get-dbaas-service-thanos",
			"exoscale/v2.get-dbaas-service-type",
			"exoscale/v2.get-dbaas-service-valkey",
			"exoscale/v2.get-dbaas-settings-clickhouse",
			"exoscale/v2.get-dbaas-settings-grafana",
			"exoscale/v2.get-dbaas-settings-kafka",
			"exoscale/v2.get-dbaas-settings-mysql",
			"exoscale/v2.get-dbaas-settings-opensearch",
			"exoscale/v2.get-dbaas-settings-pg",
			"exoscale/v2.get-dbaas-settings-thanos",
			"exoscale/v2.get-dbaas-settings-valkey",
			"exoscale/v2.get-dbaas-task",
			"exoscale/v2.list-dbaas-clickhouse-roles",
			"exoscale/v2.list-dbaas-clickhouse-users",
			"exoscale/v2.list-dbaas-external-endpoint-types",
			"exoscale/v2.list-dbaas-external-endpoints",
			"exoscale/v2.list-dbaas-external-integrations",
			"exoscale/v2.list-dbaas-integration-settings",
			"exoscale/v2.list-dbaas-integration-types",
			"exoscale/v2.list-dbaas-service-types",
			"exoscale/v2.list-dbaas-services",
			"exoscale/v2.list-dbaas-valkey-users",
			"exoscale/v2.reset-dbaas-clickhouse-user-password",
			"exoscale/v2.reset-dbaas-grafana-user-password",
			"exoscale/v2.reset-dbaas-kafka-user-password",
			"exoscale/v2.reset-dbaas-mysql-user-password",
			"exoscale/v2.reset-dbaas-opensearch-user-password",
			"exoscale/v2.reset-dbaas-postgres-user-password",
			"exoscale/v2.reset-dbaas-valkey-user-password",
			"exoscale/v2.reveal-dbaas-clickhouse-user-password",
			"exoscale/v2.reveal-dbaas-grafana-user-password",
			"exoscale/v2.reveal-dbaas-kafka-connect-password",
			"exoscale/v2.reveal-dbaas-kafka-user-password",
			"exoscale/v2.reveal-dbaas-mysql-user-password",
			"exoscale/v2.reveal-dbaas-opensearch-user-password",
			"exoscale/v2.reveal-dbaas-postgres-user-password",
			"exoscale/v2.reveal-dbaas-thanos-user-password",
			"exoscale/v2.reveal-dbaas-valkey-user-password",
			"exoscale/v2.start-dbaas-clickhouse-maintenance",
			"exoscale/v2.start-dbaas-grafana-maintenance",
			"exoscale/v2.start-dbaas-kafka-maintenance",
			"exoscale/v2.start-dbaas-mysql-maintenance",
			"exoscale/v2.start-dbaas-opensearch-maintenance",
			"exoscale/v2.start-dbaas-pg-maintenance",
			"exoscale/v2.start-dbaas-thanos-maintenance",
			"exoscale/v2.start-dbaas-valkey-maintenance",
			"exoscale/v2.stop-dbaas-mysql-migration",
			"exoscale/v2.stop-dbaas-pg-migration",
			"exoscale/v2.stop-dbaas-valkey-migration",
			"exoscale/v2.update-dbaas-external-endpoint-datadog",
			"exoscale/v2.update-dbaas-external-endpoint-elasticsearch",
			"exoscale/v2.update-dbaas-external-endpoint-opensearch",
			"exoscale/v2.update-dbaas-external-endpoint-prometheus",
			"exoscale/v2.update-dbaas-external-endpoint-rsyslog",
			"exoscale/v2.update-dbaas-external-integration-settings-datadog",
			"exoscale/v2.update-dbaas-integration",
			"exoscale/v2.update-dbaas-opensearch-acl-config",
			"exoscale/v2.update-dbaas-pg-connection-pool",
			"exoscale/v2.update-dbaas-postgres-allow-replication",
			"exoscale/v2.update-dbaas-service-clickhouse",
			"exoscale/v2.update-dbaas-service-grafana",
			"exoscale/v2.update-dbaas-service-kafka",
			"exoscale/v2.update-dbaas-service-mysql",
			"exoscale/v2.update-dbaas-service-opensearch",
			"exoscale/v2.update-dbaas-service-pg",
			"exoscale/v2.update-dbaas-service-thanos",
			"exoscale/v2.update-dbaas-service-valkey",
			"exoscale/v2.update-dbaas-valkey-user-access-control"),

		// Scalable Kubernetes Service: clusters, nodepools, their upgrades and
		// their kubeconfig.
		emulator.Because("a managed Kubernetes control plane on hosts the emulator does not run, so a kubeconfig it issued would point nowhere",
			"exoscale/v2.create-sks-cluster",
			"exoscale/v2.create-sks-nodepool",
			"exoscale/v2.delete-sks-cluster",
			"exoscale/v2.delete-sks-nodepool",
			"exoscale/v2.evict-sks-nodepool-members",
			"exoscale/v2.generate-sks-cluster-kubeconfig",
			"exoscale/v2.generate-sks-karpenter-exoscale-nodeclass",
			"exoscale/v2.generate-sks-karpenter-nodepool",
			"exoscale/v2.get-active-nodepool-template",
			"exoscale/v2.get-sks-cluster",
			"exoscale/v2.get-sks-cluster-authority-cert",
			"exoscale/v2.get-sks-cluster-inspection",
			"exoscale/v2.get-sks-nodepool",
			"exoscale/v2.list-sks-cluster-deprecated-resources",
			"exoscale/v2.list-sks-cluster-versions",
			"exoscale/v2.list-sks-clusters",
			"exoscale/v2.rotate-sks-ccm-credentials",
			"exoscale/v2.rotate-sks-csi-credentials",
			"exoscale/v2.rotate-sks-karpenter-credentials",
			"exoscale/v2.rotate-sks-operators-ca",
			"exoscale/v2.scale-sks-nodepool",
			"exoscale/v2.update-sks-cluster",
			"exoscale/v2.update-sks-nodepool",
			"exoscale/v2.upgrade-sks-cluster",
			"exoscale/v2.upgrade-sks-cluster-service-level"),

		// The one refusal of the snapshot family (#173). Every other operation in
		// it is served; this one hands back a pre-signed URL into an object
		// store, and this project does not emulate Object Storage
		// (docs/limits.md).
		//
		// Refused rather than answered with an empty export object, because the
		// difference is what a client does next: an empty object says the export
		// happened and produced nothing, and a URL that resolves to nothing gets
		// followed. A refusal is read.
		emulator.Because("it answers a pre-signed URL into Object Storage, which this project does not emulate, and a URL resolving to nothing is worse than a refusal because a client follows it",
			"exoscale/v2.export-snapshot"),

		// The AI surface: models, deployments, the inference engine.
		emulator.Because("inference needs the accelerators and the model weights Exoscale hosts, neither of which exists on this station",
			"exoscale/v2.create-ai-api-key",
			"exoscale/v2.get-user-org-consumption-quota",
			"exoscale/v2.reveal-deployment-api-key",
			"exoscale/v2.create-deployment",
			"exoscale/v2.create-model",
			"exoscale/v2.delete-ai-api-key",
			"exoscale/v2.delete-deployment",
			"exoscale/v2.delete-model",
			"exoscale/v2.get-ai-api-key",
			"exoscale/v2.get-deployment",
			"exoscale/v2.get-deployment-logs",
			"exoscale/v2.get-inference-engine-help",
			"exoscale/v2.get-model",
			"exoscale/v2.list-ai-api-keys",
			"exoscale/v2.list-ai-instance-types",
			"exoscale/v2.list-deployments",
			"exoscale/v2.list-models",
			"exoscale/v2.reveal-ai-api-key",
			"exoscale/v2.rotate-ai-api-key",
			"exoscale/v2.scale-deployment",
			"exoscale/v2.update-ai-api-key",
			"exoscale/v2.update-deployment"),

		// Key management: keys, their material, and the operations that use
		// them — encrypt, decrypt, re-encrypt, data-key derivation.
		emulator.Because("a key management service that emulated its own cryptography would return ciphertext no real key ever produced, which is worse than refusing",
			"exoscale/v2.cancel-kms-key-deletion",
			"exoscale/v2.create-kms-key",
			"exoscale/v2.decrypt",
			"exoscale/v2.disable-kms-key",
			"exoscale/v2.disable-kms-key-rotation",
			"exoscale/v2.enable-kms-key",
			"exoscale/v2.enable-kms-key-rotation",
			"exoscale/v2.encrypt",
			"exoscale/v2.generate-data-key",
			"exoscale/v2.get-kms-key",
			"exoscale/v2.list-kms-key-rotations",
			"exoscale/v2.list-kms-keys",
			"exoscale/v2.re-encrypt",
			"exoscale/v2.replicate-kms-key",
			"exoscale/v2.rotate-kms-key",
			"exoscale/v2.schedule-kms-key-deletion"),

		// Managed DNS: domains, records, and reverse records for instances and
		// elastic IPs.
		emulator.Because("authoritative DNS is a public service with real resolvers behind it, and nothing here answers a query from the internet",
			"exoscale/v2.create-dns-domain",
			"exoscale/v2.create-dns-domain-record",
			"exoscale/v2.delete-dns-domain",
			"exoscale/v2.delete-dns-domain-record",
			"exoscale/v2.delete-reverse-dns-elastic-ip",
			"exoscale/v2.delete-reverse-dns-instance",
			"exoscale/v2.get-dns-domain",
			"exoscale/v2.get-dns-domain-record",
			"exoscale/v2.get-dns-domain-zone-file",
			"exoscale/v2.get-reverse-dns-elastic-ip",
			"exoscale/v2.get-reverse-dns-instance",
			"exoscale/v2.list-dns-domain-records",
			"exoscale/v2.list-dns-domains",
			"exoscale/v2.update-dns-domain-record",
			"exoscale/v2.update-reverse-dns-elastic-ip",
			"exoscale/v2.update-reverse-dns-instance"),

		// Identity and access: roles, policies, API keys, organisation users.
		//
		// Same reason as the Outscale pack, one API over: this emulator accepts
		// every credential on purpose — SECURITY.md says so and the roadmap
		// lists verifying signatures under Not planned.
		emulator.Because("the emulator accepts every credential on purpose, so serving roles and policies would describe an access control that nothing here applies",
			"exoscale/v2.assume-iam-role",
			"exoscale/v2.create-api-key",
			"exoscale/v2.create-iam-role",
			"exoscale/v2.create-user",
			"exoscale/v2.delete-api-key",
			"exoscale/v2.delete-iam-role",
			"exoscale/v2.delete-user",
			"exoscale/v2.get-api-key",
			"exoscale/v2.get-iam-organization-policy",
			"exoscale/v2.get-iam-role",
			"exoscale/v2.list-api-keys",
			"exoscale/v2.list-iam-roles",
			"exoscale/v2.list-users",
			"exoscale/v2.reset-iam-organization-policy",
			"exoscale/v2.update-iam-organization-policy",
			"exoscale/v2.update-iam-role",
			"exoscale/v2.update-iam-role-policy",
			"exoscale/v2.update-user-role"),

		// Simple Object Storage.
		emulator.Because("object storage is refused across this project for a measured reason: clients build the S3 endpoint in code, so serving it needs DNS interception and a certificate, recorded in docs/limits.md",
			"exoscale/v2.get-sos-presigned-url",
			"exoscale/v2.list-sos-buckets-usage"),

		// The instance console and its passwords.
		//
		// The console proxy URL the real API answers carries a token onto
		// console-proxy-<zone>.exoscale.com, a host that streams the VNC of a
		// machine Exoscale runs; nothing here answers on such a host, so a URL
		// would be a promise the emulator cannot keep. The passwords are the
		// same shape one layer down: machines here are opened by the SSH key
		// the client registered, no password exists, and inventing one would
		// claim an access nothing grants — the exact lie this project exists
		// to avoid.
		emulator.Because("there is no console to proxy and no password to reveal: machines here are opened by the registered SSH key, and a URL or a password the emulator invented would claim an access nothing answers",
			"exoscale/v2.get-console-proxy-url",
			"exoscale/v2.reveal-instance-password",
			"exoscale/v2.reset-instance-password"),

		// The organisation's own consumption: balance, usage, environmental
		// impact.
		//
		// Quotas are not here because they are served now: `exo limits` reads
		// them, and it died on a 404 for two releases. A quota is a limit this
		// emulator can state as its own claim, the way it states a catalogue,
		// and a usage it can count. The organisation itself is still not here.
		emulator.Because("there is no account behind this emulator and no consumption to report, so any figure here would be one it invented and somebody planned against",
			"exoscale/v2.get-env-impact",
			"exoscale/v2.get-impact-estimate",
			"exoscale/v2.get-impact-report",
			"exoscale/v2.get-live-balance",
			"exoscale/v2.get-usage-report"),
	)
}

// guardSplitClients refuses a client this emulator can only half serve.
//
// The Terraform provider honours EXOSCALE_API_ENDPOINT for its egoscale v3
// client and builds a v2 client with no endpoint option at all. So an apply
// does not fail and does not work: it splits. Some resources answer from here,
// and the rest are created on the real cloud, in one run, with whatever
// credentials the environment holds. Measured on 0.70.0, with outbound traffic
// routed to a proxy that was not listening: `exoscale_ssh_key` tried
// `https://api-ch-gva-2.exoscale.com/v2/ssh-key` with the variable set.
//
// Half-serving that client is the worst of the three outcomes. A clean refusal
// tells the operator what is happening, at the moment it happens, in the
// client's own dialect; a half-success is indistinguishable from working until
// the invoice.
//
// The escape hatch is named rather than hidden: someone who understands the
// split and wants the v3 half anyway sets FEINT_EXOSCALE_ALLOW_TERRAFORM=1, and
// owns what the other half does. A guard with no way past it gets worked around
// by copying the emulator, which teaches nothing.
//
// TestTheTerraformProviderIsRefused fails without this.
func (p *Pack) guardSplitClients(routes []emulator.Route) []emulator.Route {
	for i := range routes {
		handler := routes[i].Handler
		routes[i].Handler = func(w http.ResponseWriter, r *http.Request) {
			if splitClient(r.UserAgent()) && os.Getenv("FEINT_EXOSCALE_ALLOW_TERRAFORM") == "" {
				writeError(w, http.StatusBadRequest,
					"the Exoscale Terraform provider only honours EXOSCALE_API_ENDPOINT for half of "+
						"its calls: the rest reach the real cloud and create billable resources. "+
						"feint refuses rather than serve half an apply. Set "+
						"FEINT_EXOSCALE_ALLOW_TERRAFORM=1 if you understand that and want the half "+
						"it can serve. See docs/limits.md.")
				return
			}
			handler(w, r)
		}
	}
	return routes
}

// splitClient recognises the Terraform provider by the user agent it sets
// itself: `Exoscale-Terraform-Provider/<version> …`. The exo CLI sends its own
// and is served normally, which is the point — this refuses one client, not a
// product.
func splitClient(userAgent string) bool {
	return strings.Contains(userAgent, "Exoscale-Terraform-Provider")
}

// apiPrefix is the whole of Exoscale's URL space here: their API description
// declares /v2 as the server path, so every route sits under it.
const pathPrefix = "/v2/"

// Prefixes implements emulator.Unrouted.
func (p *Pack) Prefixes() []string { return []string{pathPrefix} }

// NotFound implements emulator.Unrouted.
//
// Without this, an unserved /v2 route answered net/http's "404 page not found"
// in text/plain, which no Exoscale client can decode — and with 364 operations
// still untriaged, that was the likeliest answer of all. Both other packs fixed
// this and pinned it in a test; this one was left out, which is how a pack drifts
// from the two beside it.
//
// The body is the pack's own error envelope, a bare message field, because that
// is what their API returns and inventing a richer shape would be exactly the
// rule-4 violation this project forbids.
func (p *Pack) NotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound,
		"feint does not serve "+r.URL.Path+"; see /_feint/routes for what it does")
}

type createInstanceRequest struct {
	Name         string `json:"name"`
	InstanceType struct {
		ID string `json:"id"`
	} `json:"instance-type"`
	Template struct {
		ID string `json:"id"`
	} `json:"template"`
	DiskSize int `json:"disk-size"`
	// UserData was read at boot and never declared here, so nothing ever wrote
	// it: a client's cloud-init was accepted by the API and dropped in silence.
	// Exoscale documents it as base64 encoded, and a read gives it back that way.
	UserData string `json:"user-data"`
	// The rest of what the official CLI actually sends. None of it was declared
	// here, so all of it was accepted and dropped, and the CLI then crashed
	// reading back an instance missing the keys it had just attached. The
	// unread-field report named every one of these in a single run — this is
	// exactly what it is for.
	SSHKeys []struct {
		Name string `json:"name"`
	} `json:"ssh-keys"`
	// The singular form, which their API documents beside the plural — "Instance
	// SSH Key" and "Instance SSH Keys" — with neither deprecated. Reading only
	// the plural meant a client sending one key had it accepted and dropped, and
	// the machine then booted with no key at all.
	//
	// TestExoscaleReadsBothKeyForms fails without this.
	SSHKey *struct {
		Name string `json:"name"`
	} `json:"ssh-key"`
	PublicIPAssignment string `json:"public-ip-assignment"`
	SecurebootEnabled  *bool  `json:"secureboot-enabled"`
	TPMEnabled         *bool  `json:"tpm-enabled"`
	// Measured on 2026-08-10: `exo compute instance create` sends the groups as
	// whole objects and the labels as a map, and a field left undeclared here is
	// accepted and dropped in silence — the exact defect the fields above were
	// added for.
	Labels             map[string]string `json:"labels"`
	AntiAffinityGroups []struct {
		ID string `json:"id"`
	} `json:"anti-affinity-groups"`
	SecurityGroups []struct {
		ID string `json:"id"`
	} `json:"security-groups"`
}

// keyNames gathers both forms the API accepts, in the order the client sent
// them, without repeating a name given twice.
func (r createInstanceRequest) keyNames() []string {
	seen := make(map[string]bool, len(r.SSHKeys)+1)
	var names []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	if r.SSHKey != nil {
		add(r.SSHKey.Name)
	}
	for _, key := range r.SSHKeys {
		add(key.Name)
	}
	return names
}

func (p *Pack) listInstances(w http.ResponseWriter, r *http.Request) {
	list := p.env.Store.List(kindInstance, resource.Tenant{Provider: Name})
	instances := make([]map[string]any, 0, len(list))
	for _, res := range list {
		// A virtual machine gets its address tens of seconds after it starts,
		// so a create cannot wait for one. It is filled in here instead, on the
		// read a client is making anyway — which makes a list a writer too, and
		// it is held per instance rather than across the whole listing: one lock
		// for the loop would queue every instance behind the slowest runtime
		// answer, and the exclusion this needs is per target (#211).
		//
		// Observe re-reads inside the hold, so the copy rendered here cannot
		// predate the lock. A resource deleted meanwhile drops out of the
		// answer rather than being resurrected by the read.
		fresh, err := p.binding().Observe(p.env.Store, p.env.Now, kindInstance, res.ID,
			func(res *resource.Resource) bool { return p.refresh(r.Context(), res) })
		if err != nil {
			continue
		}
		instances = append(instances, p.view(fresh))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"instances": instances})
}

func (p *Pack) createInstance(w http.ResponseWriter, r *http.Request) {
	var req createInstanceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// What contracts/exoscale.json declares required, and what the pack checked
	// none of: an instance created with only a name answered 200 with
	// template {"id": ""} and instance-type {"id": ""}, and `exo compute
	// instance list` then stopped listing at it — four instances in the store,
	// one in the CLI's answer. docs/limits.md accepts *plausible but unknown*
	// ids; an absent reference is a different class, and upstream refuses it.
	//
	// TestExoscaleRefusesAnIncompleteCreate fails without this.
	switch {
	case req.Template.ID == "":
		writeError(w, http.StatusBadRequest, "template is required")
		return
	case req.InstanceType.ID == "":
		writeError(w, http.StatusBadRequest, "instance-type is required")
		return
	case req.DiskSize <= 0:
		writeError(w, http.StatusBadRequest, "disk-size is required")
		return
	}
	if strings.ContainsAny(req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}
	if len(req.UserData) > cloudinit.MaxUserData {
		writeError(w, http.StatusBadRequest, "user-data is limited to 500 kibibytes")
		return
	}

	now := p.env.Now()
	id := p.env.NewID()
	res := resource.New(id, kindInstance, resource.Tenant{Provider: Name}, stoppedState, now)
	res.Attrs = map[string]any{
		"name":          req.Name,
		"instance-type": map[string]any{"id": req.InstanceType.ID},
		"template":      map[string]any{"id": req.Template.ID},
		"disk-size":     req.DiskSize,
		// Echoed back because a client that attached them expects to read
		// them, and because their absence is not "no keys" to a client, it
		// is a missing field.
		"ssh-keys":             p.sshKeyRefs(req.keyNames()),
		"public-ip-assignment": orDefault(req.PublicIPAssignment, "inet4"),
		"secureboot-enabled":   boolOr(req.SecurebootEnabled, false),
		"tpm-enabled":          boolOr(req.TPMEnabled, false),
		// Present even when empty — {} was measured on an instance created
		// with no label, and an absent key is a different claim.
		"labels": labelsToAttr(req.Labels),
		// A stable address derived from the identifier: the real API
		// publishes one per instance, and two runs must answer the same.
		"mac-address": macAddressOf(id),
	}
	if req.UserData != "" {
		res.Attrs["user-data"] = req.UserData
	}
	// A public address, assigned at creation, because that is what the real
	// cloud does: an instance created with nothing but a type and a template
	// answers `ip_address` on the first read — measured on a real account,
	// 151.145.198.51, with no private network and no elastic IP.
	//
	// The pack recorded the intent above and never honoured it, so an emulated
	// instance published nothing and the machine took whatever the runtime's
	// default profile gave it, on the operator's own bridge. A machine carries
	// the addresses its provider's API publishes and no others (#202), and
	// "none published" was not the honest half of that rule, it was the pack
	// failing to be its own cloud.
	//
	// TestAnInstanceIsGivenAPublicAddressAtCreation fails without this.
	if assignment, _ := res.Attrs["public-ip-assignment"].(string); assignment != "none" {
		unlock := p.lockAddresses()
		if ip, ok := p.freeElasticAddress(); ok {
			res.Attrs["public-ip"] = ip
		}
		unlock()
	}
	if ids := refIDs(req.AntiAffinityGroups); len(ids) > 0 {
		res.Attrs[attrAntiAffinityGroupIDs] = ids
	}
	// An instance created without naming a group wears the default one, which
	// is what the recording shows a fresh account doing.
	p.ensureDefaultSecurityGroup()
	if ids := refIDs(req.SecurityGroups); len(ids) > 0 {
		res.Attrs[attrSecurityGroupIDs] = ids
	} else {
		res.Attrs[attrSecurityGroupIDs] = []any{defaultSecurityGroupID}
	}
	// The address comes from the machine that starts, not from a constant. It
	// used to be a fixed 203.0.113.10 on every instance: two of them reported
	// the same address and nothing answered on either.
	p.start(r.Context(), res)
	p.env.Store.Put(res)

	p.writeOperation(w, p.operationReferring(nounInstance, res.ID))
}

// refIDs keeps the identifiers of the group objects a create names, in the
// []any form attrs store.
func refIDs(refs []struct {
	ID string `json:"id"`
}) []any {
	out := make([]any, 0, len(refs))
	for _, ref := range refs {
		if ref.ID != "" {
			out = append(out, ref.ID)
		}
	}
	return out
}

// macAddressOf derives a locally-administered MAC from the instance identifier,
// so it is stable across reads without storing entropy: 06 is the measured
// first octet of what the real API hands out.
func macAddressOf(id string) string {
	hex := strings.Map(func(r rune) rune {
		if r == '-' {
			return -1
		}
		return r
	}, id)
	if len(hex) < 10 {
		hex = hex + "0000000000"
	}
	return "06:" + hex[0:2] + ":" + hex[2:4] + ":" + hex[4:6] + ":" + hex[6:8] + ":" + hex[8:10]
}

func (p *Pack) getInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// This read writes: the address arrives tens of seconds after the start, and
	// the refresh is what publishes it. Held through the shared Observe, so a
	// stop landing while the runtime answers is not undone by a GET that began
	// before it (#211).
	res, err := p.binding().Observe(p.env.Store, p.env.Now, kindInstance, id,
		func(res *resource.Resource) bool { return p.refresh(r.Context(), res) })
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.view(res))
}

func (p *Pack) deleteInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Held across the destroy and the delete. Destroying a machine runs outside
	// the store lock, and nothing orders two writers on one instance: the same
	// race the two other packs carry, and the reason this lock lives in the
	// shared binding rather than in one pack that remembered it.
	unlock := p.binding().Serialise(id)
	defer unlock()

	res, ok := p.env.Store.Get(Name, kindInstance, id)
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// Refused before anything is destroyed. The flag is the one
	// add-instance-protection set; the view never carries it, so this is the
	// only reader. TestAProtectedInstanceRefusesItsDelete fails without this.
	if protected, _ := res.Attrs[attrProtected].(bool); protected {
		writeError(w, http.StatusBadRequest, "the instance is protected against deletion; remove the protection first")
		return
	}
	// The machine goes with the instance: a leftover container would outlive
	// the resource that justified it and hold the name the next one wants.
	p.destroy(r.Context(), res)
	p.env.Store.Delete(Name, kindInstance, id)
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

// getOperation answers the poll that follows every mutation. Operations are
// stored so a client that polls twice gets the same answer.
func (p *Pack) getOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, ok := p.env.Store.Get(Name, kindOperation, id)
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, res.Attrs)
}

// The Operation envelopes, all three measured against the real API through
// `feint proxy` on 2026-08-10. They differ per mutation family, and the
// difference is not decoration — the Terraform provider blocks on client.Wait
// and reads the reference to learn what finished:
//
//   - most mutations answer {id, state, reference: {command, id, link}}, where
//     command names the *read* that follows ("get-instance", never
//     "create-instance": an earlier version of this pack put the mutation
//     there, which no recorded exchange ever showed);
//   - ssh-key register and delete answer a bare {id, state};
//   - start and stop answer {id, state, resource: {id, type}} on the live API,
//     a field their published schema does not declare and egoscale cannot
//     decode — see startInstance for why this emulator answers the reference
//     envelope there instead.
//
// The real API answers state "pending" and flips to "success" on the poll;
// this emulator applies changes synchronously and answers "success" at once,
// which every polling client accepts as the terminal state it waits for.
//
// maxOperations bounds what a long session accumulates.
//
// Every mutation stores an Operation and nothing ever removed one, so a session
// driving Terraform in a loop grew the store without limit and inflated the
// resource count /_feint/health reports. Upstream expires them too; the number
// here is the emulator's own, large enough that a client polling the operation it
// just started always finds it.
const maxOperations = 512

// operationReferring is the common envelope: the reference names the read that
// follows the mutation, and the link is its address.
func (p *Pack) operationReferring(noun, id string) map[string]any {
	return p.storeOperation(map[string]any{
		"id":    p.env.NewID(),
		"state": "success",
		"reference": map[string]any{
			"command": "get-" + noun,
			"id":      id,
			"link":    apiPrefix + "/" + noun + "/" + id,
		},
	})
}

// operationBare is what the ssh-key mutations answer: an identifier and a
// state, nothing else.
func (p *Pack) operationBare() map[string]any {
	return p.storeOperation(map[string]any{
		"id":    p.env.NewID(),
		"state": "success",
	})
}

func (p *Pack) storeOperation(op map[string]any) map[string]any {
	now := p.env.Now()
	p.env.Store.Put(&resource.Resource{
		ID:      op["id"].(string),
		Kind:    kindOperation,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "success",
		Created: now,
		Updated: now,
		Attrs:   op,
	})
	p.trimOperations()
	return op
}

// writeOperation is the one line every mutation ends on.
func (p *Pack) writeOperation(w http.ResponseWriter, op map[string]any) {
	emulator.WriteJSON(w, http.StatusOK, op)
}

// trimOperations drops the oldest operations past the bound. Oldest first,
// because the one a client is polling is the one just created.
func (p *Pack) trimOperations() {
	all := p.env.Store.List(kindOperation, resource.Tenant{Provider: Name})
	if len(all) <= maxOperations {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Created.Before(all[j].Created) })
	for _, res := range all[:len(all)-maxOperations] {
		p.env.Store.Delete(Name, kindOperation, res.ID)
	}
}

// view renders an instance exactly as the real API was measured rendering one
// on 2026-08-10, omission rules included.
//
// Two of its earlier habits were wrong in opposite directions, and only the
// recording could say so. It expanded instance-type and template into whole
// catalogue entries "so a client reading instance-type.size finds it" — the
// real answer is a bare {id}, and the CLI resolves the type itself with a
// second call. And it copied every stored attribute onto the wire, where the
// real answer emits empty arrays for its relations (a key that is absent tells
// a client the emulator does not model it) but keeps internal state off it.
func (p *Pack) view(res *resource.Resource) map[string]any {
	typeRef, _ := res.Attrs["instance-type"].(map[string]any)
	templateRef, _ := res.Attrs["template"].(map[string]any)
	labels, _ := res.Attrs["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	out := map[string]any{
		"id":            res.ID,
		"state":         res.State,
		"name":          res.Attrs["name"],
		"created-at":    res.Created.Format(time.RFC3339),
		"instance-type": map[string]any{"id": typeRef["id"]},
		"template":      map[string]any{"id": templateRef["id"]},
		"disk-size":     res.Attrs["disk-size"],
		"labels":        labels,
		"mac-address":   res.Attrs["mac-address"],
		// The emulated volume carries no encryption, but the claim here is
		// Exoscale's own: every measured instance answered true, theirs being
		// encrypted at rest by default.
		"disk-encrypted":                          true,
		"public-ip-assignment":                    res.Attrs["public-ip-assignment"],
		"secureboot-enabled":                      res.Attrs["secureboot-enabled"],
		"tpm-enabled":                             res.Attrs["tpm-enabled"],
		"application-consistent-snapshot-enabled": false,
		// Present and empty rather than absent: this is what the real API
		// answers for an instance that has none. block-storage-volumes was
		// measured beside them and stays off: their published schema does not
		// declare it on instance, and the contract check enforces that schema
		// as closed. private-networks became a computed relation with batch 3;
		// snapshots stay ahead, in batch 4.
		"private-networks":     p.privateNetworkRefs(res),
		"snapshots":            []any{},
		"anti-affinity-groups": p.antiAffinityGroupRefs(res),
		"security-groups":      securityGroupRefs(res),
		"elastic-ips":          p.elasticIPRefs(res),
	}
	if keys, _ := res.Attrs["ssh-keys"].([]any); len(keys) > 0 {
		out["ssh-keys"] = keys
		out["ssh-key"] = keys[0]
	}
	if userData, _ := res.Attrs["user-data"].(string); userData != "" {
		out["user-data"] = userData
	}
	// Exoscale's instance schema declares public-ip, so the address the machine
	// answers on is published there rather than invented.
	//
	// The stored one wins over the runtime's reading, and the order matters. The
	// pack allocates the address at creation, the way the real cloud does, so it
	// is a fact about the instance from the moment it exists — before any machine
	// boots, and with no runtime configured at all, which is the CI case. The
	// runtime's reading is how the machine ends up carrying it, not how it is
	// decided; reading it first published nothing on every control-plane-only
	// run.
	if address, _ := res.Attrs["public-ip"].(string); address != "" {
		out["public-ip"] = address
	} else if address := p.addressOf(res); address != "" {
		out["public-ip"] = address
	}
	return out
}

// antiAffinityGroupRefs resolves the groups an instance wears to the measured
// member shape, {description, id, name}. A group that no longer exists is
// dropped: the relation is computed, never stored, so it cannot go stale.
func (p *Pack) antiAffinityGroupRefs(res *resource.Resource) []any {
	out := make([]any, 0)
	for _, id := range stringList(res.Attrs[attrAntiAffinityGroupIDs]) {
		group, ok := p.env.Store.Get(Name, kindAntiAffinityGroup, id)
		if !ok {
			continue
		}
		ref := map[string]any{"id": group.ID, "name": group.Attrs["name"]}
		if description, _ := group.Attrs["description"].(string); description != "" {
			ref["description"] = description
		}
		out = append(out, ref)
	}
	return out
}

// securityGroupRefs is the measured member shape: bare {id}, name resolved by
// the client itself.
func securityGroupRefs(res *resource.Resource) []any {
	out := make([]any, 0)
	for _, id := range stringList(res.Attrs[attrSecurityGroupIDs]) {
		out = append(out, map[string]any{"id": id})
	}
	return out
}

// elasticIPRefs emits bare {id} members. The live API answers {id, ip} —
// measured — but their elastic-ip-ref schema declares id alone, and the
// contract check enforces it as closed: the same live-ahead-of-spec gap as the
// operation's resource field, resolved the same way. The CLI reads the ip with
// its own get-elastic-ip call.
func (p *Pack) elasticIPRefs(res *resource.Resource) []any {
	out := make([]any, 0)
	for _, id := range stringList(res.Attrs[attrElasticIPIDs]) {
		if _, ok := p.env.Store.Get(Name, kindElasticIP, id); !ok {
			continue
		}
		out = append(out, map[string]any{"id": id})
	}
	return out
}

// sshKeyRefs turns what the client attached into the shape their schema
// declares: an array of ssh-key, which is a name and a fingerprint. The
// fingerprint is looked up rather than invented, and a key the account does not
// hold keeps its name with an empty fingerprint instead of vanishing — dropping
// it would tell the client its request was ignored.
func (p *Pack) sshKeyRefs(requested []string) []any {
	out := make([]any, 0, len(requested))
	for _, name := range requested {
		fingerprint := ""
		if res, ok := p.env.Store.Get(Name, kindSSHKey, name); ok {
			fingerprint, _ = res.Attrs["fingerprint"].(string)
		}
		out = append(out, map[string]any{"name": name, "fingerprint": fingerprint})
	}
	return out
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

// writeError emits the Exoscale error envelope: a bare message field.
func writeError(w http.ResponseWriter, status int, message string) {
	emulator.WriteJSON(w, status, map[string]any{"message": message})
}

// Env implements emulator.Pack.
//
// This is the provider where an environment is not enough, and the Note field
// exists because of it. Both halves of what this comment used to say were
// measured, and both had become false in opposite directions.
//
// The exo CLI *does* read EXOSCALE_API_ENDPOINT. That was reported by a
// contributor in #51, with the line of their source that reads it, and verified
// here in both directions on exo 1.95.1: pointed at the emulator it drives it,
// pointed at a dead port it fails naming that port. The older claim came from a
// logging proxy, and describes a version that no longer matters.
//
// The Terraform provider reads it for half of itself, which is worse than
// either answer. exoscale/exoscale 0.70.0 builds two clients in
// pkg/provider/provider.go: an egoscale v3 one, where the variable is honoured
//
//	if ep := os.Getenv("EXOSCALE_API_ENDPOINT"); ep != "" {
//		opts = append(opts, exov3.ClientOptWithEndpoint(exov3.Endpoint(ep)), …)
//	}
//
// and an egoscale v2 one, created with no endpoint option at all. Whatever a
// resource reaches through v2 goes to the real cloud: an audit watched
// `terraform apply` send GET api-ch-dk-2.exoscale.com/v2/template and POST
// api-ch-gva-2.exoscale.com/v2/ssh-key with the variable set, and
// `https://api-ch-gva-2.exoscale.com/v2` is compiled into the binary.
//
// So a plan does not fail cleanly, it splits: some resources answer from the
// emulator and some are created for real, in one apply. That is the dangerous
// shape, and it is what the note says. A user who runs
// `eval "$(feint env exoscale)"` with real credentials still in their
// environment, and then `terraform apply`, creates billable resources while
// believing they drive an emulator. Only the fake credentials' bad signature
// stopped it when it was measured. tools/conformance/guard.sh exists because a
// version of this accident already happened to this repository.
//
// Both earlier versions of this comment were wrong, in opposite directions, and
// each was written from one measurement: a logging proxy said the CLI reads
// nothing, an audit said Terraform reaches the real cloud. Their provider's own
// source settles it, which is what CLAUDE.md says to read when a client
// surprises you.
//
// TestEnvSendsItsNoteToStderr in internal/cli fails without the warning.
func (p *Pack) Env(endpoint string) emulator.Environment {
	return emulator.Environment{
		Vars: map[string]string{
			"EXOSCALE_API_KEY":      "EXOxxxxxxxxxxxxxxxxxxxx",
			"EXOSCALE_API_SECRET":   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"EXOSCALE_API_ENDPOINT": endpoint + apiPrefix,
			"EXOSCALE_ZONE":         zoneName,
		},
		Note: "the exo CLI reads these. The Terraform provider only half does: it honours " +
			"EXOSCALE_API_ENDPOINT for its egoscale v3 client and not for its v2 one, so an " +
			"apply splits between this emulator and the real cloud — with real credentials in " +
			"your environment it will create billable resources. Do not point terraform at " +
			"Exoscale here. See docs/limits.md.",
	}
}

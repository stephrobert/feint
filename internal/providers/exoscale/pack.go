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
	"slices"
	"sort"
	"time"

	"github.com/stephrobert/feint/internal/core/cloudinit"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Name is the provider key.
const Name = "exoscale"

const (
	kindInstance  = "compute-instance"
	kindOperation = "operation"
)

// Pack implements emulator.Pack for Exoscale.
type Pack struct {
	env *emulator.Env
}

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
	return []emulator.Route{
		{Method: "GET", Path: "/v2/instance", Operation: operation("list-instances"), Handler: p.listInstances},
		{Method: "POST", Path: "/v2/instance", Operation: operation("create-instance"), Handler: p.createInstance},
		{Method: "GET", Path: "/v2/instance/{id}", Operation: operation("get-instance"), Handler: p.getInstance},
		{Method: "DELETE", Path: "/v2/instance/{id}", Operation: operation("delete-instance"), Handler: p.deleteInstance},
		{Method: "GET", Path: "/v2/operation/{id}", Operation: operation("get-operation"), Handler: p.getOperation},

		// The first call the official CLI makes, and the address every call
		// after it uses. Measured, not assumed: see catalog.go.
		{Method: "GET", Path: "/v2/zone", Operation: operation("list-zones"), Handler: p.listZones},
		{Method: "GET", Path: "/v2/template", Operation: operation("list-templates"), Handler: p.listTemplates},
		{Method: "GET", Path: "/v2/template/{id}", Operation: operation("get-template"), Handler: p.getTemplate},
		{Method: "GET", Path: "/v2/instance-type", Operation: operation("list-instance-types"), Handler: p.listInstanceTypes},
		{Method: "GET", Path: "/v2/instance-type/{id}", Operation: operation("get-instance-type"), Handler: p.getInstanceType},

		// SSH keys. On the critical path of a create: the official CLI
		// registers one of its own before it posts the instance.
		{Method: "POST", Path: "/v2/ssh-key", Operation: operation("register-ssh-key"), Handler: p.registerSSHKey},
		{Method: "GET", Path: "/v2/ssh-key", Operation: operation("list-ssh-keys"), Handler: p.listSSHKeys},
		{Method: "GET", Path: "/v2/ssh-key/{name}", Operation: operation("get-ssh-key"), Handler: p.getSSHKey},
		{Method: "DELETE", Path: "/v2/ssh-key/{name}", Operation: operation("delete-ssh-key"), Handler: p.deleteSSHKey},
	}
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

		// The organisation's own consumption: balance, usage, environmental
		// impact.
		//
		// Quotas are deliberately NOT here, and neither is the organisation
		// itself. An audit caught them swept in: docs/roadmap-exoscale-iaas.md
		// keeps them in scope as small read-only calls several clients make, and
		// `exo limits` — a first-class command of the official CLI — died on the
		// refusal. A quota is a limit this emulator can state as its own claim,
		// the way it already states a catalogue.
		emulator.Because("there is no account behind this emulator and no consumption to report, so any figure here would be one it invented and somebody planned against",
			"exoscale/v2.get-env-impact",
			"exoscale/v2.get-impact-estimate",
			"exoscale/v2.get-impact-report",
			"exoscale/v2.get-live-balance",
			"exoscale/v2.get-usage-report"),
	)
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
	PublicIPAssignment string `json:"public-ip-assignment"`
	SecurebootEnabled  *bool  `json:"secureboot-enabled"`
	TPMEnabled         *bool  `json:"tpm-enabled"`
}

func (p *Pack) listInstances(w http.ResponseWriter, r *http.Request) {
	list := p.env.Store.List(kindInstance, resource.Tenant{Provider: Name})
	instances := make([]map[string]any, 0, len(list))
	for _, res := range list {
		// A virtual machine gets its address tens of seconds after it starts,
		// so a create cannot wait for one. It is filled in here instead, on the
		// read a client is making anyway.
		if p.refresh(r.Context(), res) && !p.env.Store.Commit(res, p.env.Now()) {
			// Deleted while its address was being discovered.
			continue
		}
		instances = append(instances, p.view(res))
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
	if len(req.UserData) > cloudinit.MaxUserData {
		writeError(w, http.StatusBadRequest, "user-data is limited to 500 kibibytes")
		return
	}

	now := p.env.Now()
	res := &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindInstance,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "stopped",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":          req.Name,
			"instance-type": map[string]any{"id": req.InstanceType.ID},
			"template":      map[string]any{"id": req.Template.ID},
			"disk-size":     req.DiskSize,
			// Echoed back because a client that attached them expects to read
			// them, and because their absence is not "no keys" to a client, it
			// is a missing field.
			"ssh-keys":             p.sshKeyRefs(req.SSHKeys),
			"public-ip-assignment": orDefault(req.PublicIPAssignment, "inet4"),
			"secureboot-enabled":   boolOr(req.SecurebootEnabled, false),
			"tpm-enabled":          boolOr(req.TPMEnabled, false),
		},
	}
	if req.UserData != "" {
		res.Attrs["user-data"] = req.UserData
	}
	// The address comes from the machine that starts, not from a constant. It
	// used to be a fixed 203.0.113.10 on every instance: two of them reported
	// the same address and nothing answered on either.
	p.start(r.Context(), res)
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, p.operationFor(res.ID, "create-instance"))
}

func (p *Pack) getInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, ok := p.env.Store.Get(Name, kindInstance, id)
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if p.refresh(r.Context(), res) && !p.env.Store.Commit(res, p.env.Now()) {
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
	// The machine goes with the instance: a leftover container would outlive
	// the resource that justified it and hold the name the next one wants.
	p.destroy(r.Context(), res)
	p.env.Store.Delete(Name, kindInstance, id)
	emulator.WriteJSON(w, http.StatusOK, p.operationFor(id, "delete-instance"))
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

// operationFor records and returns an already-successful operation.
//
// The command is passed in rather than built from a kind. It used to be
// hardcoded as "create-"+kind, so the operation a DELETE returned described
// itself as a creation — a client that reads the reference to decide what
// finished was told the opposite of what happened.
// maxOperations bounds what a long session accumulates.
//
// Every mutation stores an Operation and nothing ever removed one, so a session
// driving Terraform in a loop grew the store without limit and inflated the
// resource count /_feint/health reports. Upstream expires them too; the number
// here is the emulator's own, large enough that a client polling the operation it
// just started always finds it.
const maxOperations = 512

func (p *Pack) operationFor(referenceID, command string) map[string]any {
	now := p.env.Now()
	op := map[string]any{
		"id":    p.env.NewID(),
		"state": "success",
		"reference": map[string]any{
			"id":      referenceID,
			"command": command,
		},
	}
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

func (p *Pack) view(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+4)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["id"] = res.ID
	out["state"] = res.State
	// instance-type and template are full objects upstream, not bare
	// identifiers: their schema declares a ref, and a client reading
	// instance-type.size off what the emulator returned found nothing there.
	// Resolved from the catalogue so the two cannot disagree.
	out["instance-type"] = expand(instanceTypes, res.Attrs["instance-type"])
	out["template"] = expand(templates, res.Attrs["template"])
	out["created-at"] = res.Created.Format(time.RFC3339)
	// Exoscale's instance schema declares public-ip, so the address the machine
	// answers on is published there rather than invented.
	if address := p.addressOf(res); address != "" {
		out["public-ip"] = address
	}
	return out
}

// sshKeyRefs turns what the client attached into the shape their schema
// declares: an array of ssh-key, which is a name and a fingerprint. The
// fingerprint is looked up rather than invented, and a key the account does not
// hold keeps its name with an empty fingerprint instead of vanishing — dropping
// it would tell the client its request was ignored.
func (p *Pack) sshKeyRefs(requested []struct {
	Name string `json:"name"`
}) []any {
	out := make([]any, 0, len(requested))
	for _, want := range requested {
		fingerprint := ""
		if res, ok := p.env.Store.Get(Name, kindSSHKey, want.Name); ok {
			fingerprint, _ = res.Attrs["fingerprint"].(string)
		}
		out = append(out, map[string]any{"name": want.Name, "fingerprint": fingerprint})
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

// expand resolves a stored {"id": ...} against a catalogue and returns the whole
// entry. An identifier the catalogue does not hold keeps its bare form rather
// than disappearing: a client that asked for something unknown must see what it
// asked for, not silence.
func expand(catalogue []map[string]any, stored any) any {
	ref, ok := stored.(map[string]any)
	if !ok {
		return stored
	}
	for _, entry := range catalogue {
		if entry["id"] == ref["id"] {
			return entry
		}
	}
	return ref
}

// writeError emits the Exoscale error envelope: a bare message field.
func writeError(w http.ResponseWriter, status int, message string) {
	emulator.WriteJSON(w, status, map[string]any{"message": message})
}

// Env implements emulator.Pack.
//
// This is the provider where an environment is not enough, and the Note field
// exists because of it. Measured with a logging proxy: the official exo CLI
// reads no endpoint variable at all. It can only be redirected through the
// `endpoint` key of its own configuration file, and that value must carry the
// /v2 suffix because the CLI concatenates it with the route it wants rather than
// adding a version segment of its own.
//
// EXOSCALE_API_ENDPOINT is exported anyway: the Terraform provider does read it.
// Handing a user variables that work for one client and not another, without
// saying which, is the kind of half-answer this project exists to avoid — so the
// note goes to stderr, where `eval` cannot swallow it.
func (p *Pack) Env(endpoint string) emulator.Environment {
	return emulator.Environment{
		Vars: map[string]string{
			"EXOSCALE_API_KEY":      "EXOxxxxxxxxxxxxxxxxxxxx",
			"EXOSCALE_API_SECRET":   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"EXOSCALE_API_ENDPOINT": endpoint + apiPrefix,
			"EXOSCALE_ZONE":         zoneName,
		},
		Note: "the exo CLI ignores these: it takes its endpoint only from its config file, " +
			"where `endpoint` must be " + endpoint + apiPrefix + " — see tools/conformance/exoscale/exo-cli.sh.",
	}
}

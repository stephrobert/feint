package outscale_test

import (
	"net/http"
	"testing"
)

// The resource algebra of addresses, gateways, NAT and snapshots: what holds
// what, what refuses to go first, and which keys a pristine object omits. All
// shapes measured (X-2 sweep, 2026-08-08); all values invented here.

func TestAPublicIpIsAllocatedListedAndReleased(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreatePublicIp", `{}`)
	ip, _ := created["PublicIp"].(map[string]any)
	// Deterministic first address, from the documented-fictional TEST-NET-3
	// block ReadPublicIpRanges publishes.
	if ip["PublicIp"] != "198.51.100.1" {
		t.Fatalf("first allocation = %v, want 198.51.100.1", ip["PublicIp"])
	}
	// Measured: an unlinked address is exactly {PublicIpId, PublicIp, Tags} —
	// no Vm, Nic or NatService keys, not even empty ones.
	for _, forbidden := range []string{"VmId", "NicId", "NatServiceId", "LinkPublicIpId", "PrivateIp"} {
		if _, present := ip[forbidden]; present {
			t.Fatalf("an unlinked address carries %s, which the real cloud omits: %v", forbidden, ip)
		}
	}

	second := call(t, ts, doc, "CreatePublicIp", `{}`)
	ip2, _ := second["PublicIp"].(map[string]any)
	if ip2["PublicIp"] != "198.51.100.2" {
		t.Fatalf("second allocation = %v, want 198.51.100.2", ip2["PublicIp"])
	}

	// The catalogue and the allocator answer from the same block.
	ranges := call(t, ts, doc, "ReadPublicIpRanges", `{}`)
	if list, _ := ranges["PublicIps"].([]any); len(list) != 1 || list[0] != "198.51.100.0/24" {
		t.Fatalf("ReadPublicIpRanges does not publish the allocator's block: %v", ranges["PublicIps"])
	}

	// Deleting by address value, which the API accepts alongside the id.
	call(t, ts, doc, "DeletePublicIp", `{"PublicIp":"198.51.100.1"}`)
	left := call(t, ts, doc, "ReadPublicIps", `{}`)
	if list, _ := left["PublicIps"].([]any); len(list) != 1 {
		t.Fatalf("one address should remain: %v", left["PublicIps"])
	}
}

func TestAnInternetServiceLinksToOneNetAndRefusesToVanishLinked(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	created := call(t, ts, doc, "CreateInternetService", `{}`)
	gw, _ := created["InternetService"].(map[string]any)
	gwID, _ := gw["InternetServiceId"].(string)
	// Measured: an unlinked gateway carries neither NetId nor State — the
	// state belongs to the link.
	for _, forbidden := range []string{"NetId", "State"} {
		if _, present := gw[forbidden]; present {
			t.Fatalf("an unlinked gateway carries %s, which the real cloud omits: %v", forbidden, gw)
		}
	}

	netCreated := call(t, ts, doc, "CreateNet", `{"IpRange":"10.10.0.0/16"}`)
	net, _ := netCreated["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)

	call(t, ts, doc, "LinkInternetService", `{"InternetServiceId":"`+gwID+`","NetId":"`+netID+`"}`)
	linked := firstOf(t, call(t, ts, doc, "ReadInternetServices", `{"Filters":{"LinkNetIds":["`+netID+`"]}}`), "InternetServices")
	if linked["NetId"] != netID || linked["State"] != "available" {
		t.Fatalf("the linked gateway does not carry the measured link shape: %v", linked)
	}

	// One gateway per Net: the second link is a conflict, not a swap.
	other := call(t, ts, doc, "CreateInternetService", `{}`)
	otherGw, _ := other["InternetService"].(map[string]any)
	if status, body := post(t, ts, "LinkInternetService",
		`{"InternetServiceId":"`+otherGw["InternetServiceId"].(string)+`","NetId":"`+netID+`"}`); status != http.StatusConflict {
		t.Fatalf("a second gateway linked to the same Net: %d %v", status, body)
	}

	// Neither the linked gateway nor the Net goes while the link stands, which
	// is the refusal Terraform's destroy order retries on.
	if status, body := post(t, ts, "DeleteInternetService", `{"InternetServiceId":"`+gwID+`"}`); status != http.StatusConflict {
		t.Fatalf("a linked gateway was deleted: %d %v", status, body)
	}
	if status, body := post(t, ts, "DeleteNet", `{"NetId":"`+netID+`"}`); status != http.StatusConflict {
		t.Fatalf("a Net with a linked gateway was deleted: %d %v", status, body)
	}

	call(t, ts, doc, "UnlinkInternetService", `{"InternetServiceId":"`+gwID+`","NetId":"`+netID+`"}`)
	call(t, ts, doc, "DeleteInternetService", `{"InternetServiceId":"`+gwID+`"}`)
	call(t, ts, doc, "DeleteNet", `{"NetId":"`+netID+`"}`)
}

func TestANatServiceConsumesItsPublicIp(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	_, subnetID := netAndSubnet(t, ts, "10.11.0.0/16", "10.11.1.0/24")
	created := call(t, ts, doc, "CreatePublicIp", `{}`)
	ip, _ := created["PublicIp"].(map[string]any)
	ipID, _ := ip["PublicIpId"].(string)

	nat := call(t, ts, doc, "CreateNatService", `{"SubnetId":"`+subnetID+`","PublicIpId":"`+ipID+`"}`)
	service, _ := nat["NatService"].(map[string]any)
	natID, _ := service["NatServiceId"].(string)
	if service["SubnetId"] != subnetID || service["State"] != "available" {
		t.Fatalf("the NAT service is not the measured shape: %v", service)
	}
	held, _ := service["PublicIps"].([]any)
	if len(held) != 1 {
		t.Fatalf("the NAT service does not hold its address: %v", service)
	}

	// The address now answers with its holder — measured on a real account,
	// where an address consumed by NAT gains NatServiceId and LinkPublicIpId.
	addr := firstOf(t, call(t, ts, doc, "ReadPublicIps", `{"Filters":{"PublicIpIds":["`+ipID+`"]}}`), "PublicIps")
	if addr["NatServiceId"] != natID {
		t.Fatalf("the address does not name its holder: %v", addr)
	}

	// Held means held: the address does not release, a second NAT service does
	// not steal it, and the subnet does not vanish under the service.
	if status, body := post(t, ts, "DeletePublicIp", `{"PublicIpId":"`+ipID+`"}`); status != http.StatusConflict {
		t.Fatalf("a held address was released: %d %v", status, body)
	}
	if status, body := post(t, ts, "CreateNatService", `{"SubnetId":"`+subnetID+`","PublicIpId":"`+ipID+`"}`); status != http.StatusConflict {
		t.Fatalf("a second NAT service took a held address: %d %v", status, body)
	}
	if status, body := post(t, ts, "DeleteSubnet", `{"SubnetId":"`+subnetID+`"}`); status != http.StatusConflict {
		t.Fatalf("a subnet holding a NAT service was deleted: %d %v", status, body)
	}

	// Deleting the service releases the address, exactly as the real teardown
	// does — and the address view loses the holder keys, not just their values.
	call(t, ts, doc, "DeleteNatService", `{"NatServiceId":"`+natID+`"}`)
	released := firstOf(t, call(t, ts, doc, "ReadPublicIps", `{"Filters":{"PublicIpIds":["`+ipID+`"]}}`), "PublicIps")
	if _, present := released["NatServiceId"]; present {
		t.Fatalf("the released address still names a holder: %v", released)
	}
	call(t, ts, doc, "DeletePublicIp", `{"PublicIpId":"`+ipID+`"}`)
}

func TestAVolumeRemembersItsSnapshotAndOnlyThen(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	// A plain volume: no SnapshotId key at all. Measured — the real cloud
	// omits the key on a volume with no provenance, never sends "".
	plain := call(t, ts, doc, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	volume, _ := plain["Volume"].(map[string]any)
	if _, present := volume["SnapshotId"]; present {
		t.Fatalf("a plain volume carries a SnapshotId key the real cloud omits: %v", volume)
	}
	volumeID, _ := volume["VolumeId"].(string)

	// Its snapshot: completed at once, Progress 100 — transitions here are
	// immediate by design (docs/limits.md), and Terraform's own wait is
	// satisfied by the final state rather than by a window it has to poll
	// through.
	snapped := call(t, ts, doc, "CreateSnapshot", `{"VolumeId":"`+volumeID+`","Description":"before"}`)
	snapshot, _ := snapped["Snapshot"].(map[string]any)
	snapshotID, _ := snapshot["SnapshotId"].(string)
	if snapshot["State"] != "completed" {
		t.Fatalf("a snapshot is completed at once here: %v", snapshot)
	}
	if progress, _ := snapshot["Progress"].(float64); progress != 100 {
		t.Fatalf("Progress = %v, want 100", snapshot["Progress"])
	}

	// A volume restored from it carries the provenance and inherits the size.
	restored := call(t, ts, doc, "CreateVolume", `{"SubregionName":"eu-west-2a","SnapshotId":"`+snapshotID+`"}`)
	child, _ := restored["Volume"].(map[string]any)
	if child["SnapshotId"] != snapshotID {
		t.Fatalf("the restored volume does not name its snapshot: %v", child)
	}
	if size, _ := child["Size"].(float64); size != 10 {
		t.Fatalf("the restored volume did not inherit the size: %v", child["Size"])
	}

	// An unknown snapshot is refused, not minted — in the pack's own error
	// dialect, InvalidResource, the type a real client branches on.
	status, body := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","SnapshotId":"snap-00000000"}`)
	if status == http.StatusOK {
		t.Fatalf("an unknown SnapshotId was accepted: %v", body)
	}
	errs, _ := body["Errors"].([]any)
	if len(errs) == 0 {
		t.Fatalf("the refusal is not a decodable API error: %v", body)
	}
	if first, _ := errs[0].(map[string]any); first["Type"] != "InvalidResource" {
		t.Fatalf("the refusal is not typed InvalidResource: %v", body)
	}

	// Deleting the snapshot keeps the provenance: history, not a live
	// reference, same as the real API.
	call(t, ts, doc, "DeleteSnapshot", `{"SnapshotId":"`+snapshotID+`"}`)
	after := firstOf(t, call(t, ts, doc, "ReadVolumes", `{"Filters":{"VolumeIds":["`+child["VolumeId"].(string)+`"]}}`), "Volumes")
	if after["SnapshotId"] != snapshotID {
		t.Fatalf("the provenance vanished with the snapshot: %v", after)
	}
}

func TestTheNetCataloguesAreFixedAndFilterable(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	services := call(t, ts, doc, "ReadNetAccessPointServices", `{}`)
	if list, _ := services["Services"].([]any); len(list) != 7 {
		t.Fatalf("seven services expected, the count a real region publishes: %v", len(list))
	}
	one := firstOf(t, call(t, ts, doc, "ReadNetAccessPointServices",
		`{"Filters":{"ServiceNames":["com.outscale.eu-west-2.api"]}}`), "Services")
	if one["ServiceId"] != "pl-00000001" {
		t.Fatalf("the api service is not the fixed one: %v", one)
	}
}

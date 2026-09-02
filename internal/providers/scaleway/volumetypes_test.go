package scaleway_test

import (
	"net/http"
	"strings"
	"testing"
)

// What instance/v1 CreateVolume answers to each volume_type, measured against
// fr-par on 2026-09-02 with a real account (#393).
//
// The emulator answered 201 to all of them, and defaulted an absent type to
// b_ssd: a value the platform retired, standing in the one field a client reads
// back. The surface scan could not see it, because CreateVolume's Go signature
// never changed. Only a recording of the cloud names this kind of drift, which
// is why the corpus outranks the SDK here.
//
// The table below is the recording, transcribed. Each line is one request the
// account served.
func TestCreateVolumeRefusesTheTypesTheCloudRefuses(t *testing.T) {
	ts := newTestServer(t)

	for _, c := range []struct {
		name    string
		body    string
		status  int
		reason  string
		message string
	}{
		{
			name:    "absent",
			body:    `{"name":"probe","size":10000000000}`,
			status:  http.StatusBadRequest,
			reason:  "required",
			message: "required key not provided",
		},
		{
			name:    "b_ssd",
			body:    `{"name":"probe","volume_type":"b_ssd","size":10000000000}`,
			status:  http.StatusBadRequest,
			reason:  "constraint",
			message: "b_ssd volumes are no longer supported",
		},
		{
			// Served by block/v1, which is where the cloud moved it. The cloud
			// does not point at the other product either: it answers the same
			// flat refusal as for a type that never existed, and rule 4 says to
			// copy the wording rather than improve it.
			name:    "sbs_volume",
			body:    `{"name":"probe","volume_type":"sbs_volume","size":10000000000}`,
			status:  http.StatusBadRequest,
			reason:  "constraint",
			message: "not a valid value",
		},
		{
			name:   "l_ssd",
			body:   `{"name":"probe","volume_type":"l_ssd","size":10000000000}`,
			status: http.StatusCreated,
		},
		{
			name:   "scratch",
			body:   `{"name":"probe","volume_type":"scratch","size":10000000000}`,
			status: http.StatusCreated,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, out := do(t, ts, "POST", zoneURL+"/volumes", c.body)
			if status != c.status {
				t.Fatalf("status %d, want %d (%v)", status, c.status, out)
			}
			if c.status == http.StatusCreated {
				// Created is only half the answer: the type has to survive the
				// round trip, because an emulator that accepted l_ssd and stored
				// b_ssd would pass the status check and lie in the one field a
				// client reads back.
				volume, _ := out["volume"].(map[string]any)
				if got, _ := volume["volume_type"].(string); got != c.name {
					t.Fatalf("volume_type came back %q, want %q", got, c.name)
				}
				return
			}
			if got, _ := out["type"].(string); got != "invalid_arguments" {
				t.Fatalf("error type is %q, want invalid_arguments (%v)", got, out)
			}
			details, _ := out["details"].([]any)
			if len(details) != 1 {
				t.Fatalf("details holds %d entries, want 1 (%v)", len(details), out)
			}
			d, _ := details[0].(map[string]any)
			if got, _ := d["argument_name"].(string); got != "volume_type" {
				t.Errorf("argument_name is %q, want volume_type", got)
			}
			if got, _ := d["reason"].(string); got != c.reason {
				t.Errorf("reason is %q, want %q", got, c.reason)
			}
			if got, _ := d["help_message"].(string); !strings.Contains(got, c.message) {
				t.Errorf("help_message is %q, want it to carry %q", got, c.message)
			}
		})
	}
}

// The same fact one route further on: what POST /servers answers when its
// volumes map names a type, measured the same day against the same account.
//
// The two routes do not share a vocabulary, and that is the point of testing
// both: argument_name is volumes.0.volume_type here and volume_type there, the
// b_ssd help message points at a different product, and scratch is refused on
// argument "image" rather than on the type at all.
func TestCreateServerRefusesTheVolumeTypesTheCloudRefuses(t *testing.T) {
	ts := newTestServer(t)

	for _, c := range []struct {
		name     string
		volume   string
		status   int
		argument string
		message  string
	}{
		{
			name:     "b_ssd",
			volume:   `{"volume_type":"b_ssd","size":10000000000}`,
			status:   http.StatusBadRequest,
			argument: "volumes.0.volume_type",
			message:  "Create volumes with volume_type=sbs_volume instead",
		},
		{
			name:     "nonsense",
			volume:   `{"volume_type":"nonsense","size":10000000000}`,
			status:   http.StatusBadRequest,
			argument: "volumes.0.volume_type",
			message:  "not a valid value",
		},
		{
			// Refused on the image, not on the type: a client reading
			// argument_name is told which of its two inputs to change, and this
			// pack always resolves an image, including for a request naming
			// none.
			name:     "scratch",
			volume:   `{"volume_type":"scratch","size":10000000000}`,
			status:   http.StatusBadRequest,
			argument: "image",
			message:  "Cannot use an image with a scratch volume",
		},
		{
			name:   "sbs_volume",
			volume: `{"volume_type":"sbs_volume","size":10000000000}`,
			status: http.StatusCreated,
		},
		{
			name:   "l_ssd",
			volume: `{"volume_type":"l_ssd","size":10000000000}`,
			status: http.StatusCreated,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := `{"name":"probe","commercial_type":"DEV1-S","image":"ubuntu_jammy","volumes":{"0":` + c.volume + `}}`
			status, out := do(t, ts, "POST", zoneURL+"/servers", body)
			if status != c.status {
				t.Fatalf("status %d, want %d (%v)", status, c.status, out)
			}
			if c.status == http.StatusCreated {
				// Honoured, not replaced. This pack used to write b_ssd here
				// whatever was asked, so a create naming l_ssd came back as a
				// type the cloud no longer mints — the exact answer a client
				// reads and stores.
				server, _ := out["server"].(map[string]any)
				volumes, _ := server["volumes"].(map[string]any)
				root, _ := volumes["0"].(map[string]any)
				if got, _ := root["volume_type"].(string); got != c.name {
					t.Fatalf("the root volume came back %q, want %q", got, c.name)
				}
				return
			}
			details, _ := out["details"].([]any)
			if len(details) != 1 {
				t.Fatalf("details holds %d entries, want 1 (%v)", len(details), out)
			}
			d, _ := details[0].(map[string]any)
			if got, _ := d["argument_name"].(string); got != c.argument {
				t.Errorf("argument_name is %q, want %q", got, c.argument)
			}
			if got, _ := d["help_message"].(string); !strings.Contains(got, c.message) {
				t.Errorf("help_message is %q, want it to carry %q", got, c.message)
			}
			// A refusal that left a server behind would be the defect this
			// ordering exists to avoid, and it is invisible from the 400 alone.
			_, list := do(t, ts, "GET", zoneURL+"/servers", "")
			if servers, _ := list["servers"].([]any); len(servers) != 0 {
				t.Fatalf("the refused create left %d server(s) behind", len(servers))
			}
		})
	}
}

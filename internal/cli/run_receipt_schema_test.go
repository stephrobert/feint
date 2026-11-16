package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The JSON receipt says which faults schema the lab served (#964).
//
// The prose receipt states it since #928; the JSON one is the receipt a CI job
// keeps, and it cannot be re-read against the lab afterwards. So the number
// travels with the lines, and each case below asks one question of it, against
// a lab stub serving only what that schema really publishes (fault_schema_test.go
// says why these labs are stubs).

// jsonReceiptAgainst attaches `feint run --json -` to a lab and answers the
// receipt decoded as a map, so that an absent key and a zero are told apart.
func jsonReceiptAgainst(t *testing.T, endpoint string) map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runCommand([]string{"--endpoint", endpoint, "--json", "-", "--", "sh", "-c", "exit 3"},
		&stdout, &stderr)
	if code != 3 {
		t.Fatalf("the run answered %d where the child answered 3\nstderr:\n%s", code, stderr.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("stdout is not the receipt alone: %v\n%s", err, stdout.String())
	}
	// Every case reads a lab whose state WAS read: an unread state also omits
	// the key, and a case that passed through that branch would measure nothing.
	if receipt["faults_read"] != true {
		t.Fatalf("the lab's fault state was not read, so this case cannot be judged:\n%s", stdout.String())
	}
	return receipt
}

// TestTheJSONReceiptCarriesTheLabsFaultsSchema is cited by runReceipt.FaultsSchema.
func TestTheJSONReceiptCarriesTheLabsFaultsSchema(t *testing.T) {
	// A schema 3 lab holding a line in flight: callFaults derives
	// `in_flight: true`, and the number is what tells a reader it was derived.
	t.Run("schema 3, a derived in_flight", func(t *testing.T) {
		receipt := jsonReceiptAgainst(t, labStub(t, faultsBody(3, firedInFlight)))
		if got := receipt["faults_schema_version"]; got != float64(3) {
			t.Errorf("faults_schema_version = %v, want 3:\n%v", got, receipt)
		}
		faults, _ := receipt["faults"].([]any)
		if len(faults) != 1 || faults[0].(map[string]any)["in_flight"] != true {
			t.Fatalf("the derived in_flight line is not in the receipt, so the case cannot be judged: %v", receipt["faults"])
		}
	})

	// Schema 1, every release up to v0.16.0: no `fired` at all, so an absent
	// `faults` is not "none fired", and the number is what says so.
	t.Run("schema 1, no per-call receipt", func(t *testing.T) {
		receipt := jsonReceiptAgainst(t, labStub(t, schema1Body(1)))
		if got := receipt["faults_schema_version"]; got != float64(1) {
			t.Errorf("faults_schema_version = %v, want 1:\n%v", got, receipt)
		}
		if _, present := receipt["faults"]; present {
			t.Errorf("a schema 1 lab published fired lines it does not have: %v", receipt["faults"])
		}
	})

	// The accepting half: a lab at the schema this tree serves names it.
	t.Run("the current schema", func(t *testing.T) {
		receipt := jsonReceiptAgainst(t, labStub(t, faultsBody(emulator.FaultsSchemaVersion, firedAnswered)))
		if got := receipt["faults_schema_version"]; got != float64(emulator.FaultsSchemaVersion) {
			t.Errorf("faults_schema_version = %v, want %d:\n%v", got, emulator.FaultsSchemaVersion, receipt)
		}
	})

	// A document that names no schema: the key is absent, because the lab said
	// nothing, and a number here would be one the CLI made up.
	t.Run("no schema_version", func(t *testing.T) {
		receipt := jsonReceiptAgainst(t, labStub(t, faultsBody(-1, firedAbandoned)))
		if got, present := receipt["faults_schema_version"]; present {
			t.Errorf("a lab that names no schema is published as schema %v", got)
		}
	})

	// The receipt's own version moved with the key, which is the contract.
	t.Run("the receipt names its version", func(t *testing.T) {
		receipt := jsonReceiptAgainst(t, labStub(t, faultsBody(3, firedInFlight)))
		if got := receipt["schema_version"]; got != float64(runReceiptSchemaVersion) || runReceiptSchemaVersion < 4 {
			t.Errorf("the receipt says schema_version %v (constant %d); faults_schema_version arrived with 4",
				got, runReceiptSchemaVersion)
		}
	})
}

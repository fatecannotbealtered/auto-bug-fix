package cmd

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCommandDefaultsToJSONEnvelope(t *testing.T) {
	stdout, err := runCLIForTest(t, "status", "--compact")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, stdout)
	}
	var env jsonEnvelope
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatalf("status did not emit one JSON envelope: %v\n%s", err, stdout)
	}
	if !env.OK || env.SchemaVersion != schemaVersion || env.Data == nil {
		t.Fatalf("unexpected status envelope: %+v", env)
	}
}

func TestWriteWithoutConfirmReturnsErrorEnvelope(t *testing.T) {
	stdout, err := runCLIForTest(t, "stop", "--compact")
	if err == nil {
		t.Fatalf("stop without confirm should fail")
	}
	var env jsonEnvelope
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatalf("stop did not emit JSON failure envelope: %v\n%s", err, stdout)
	}
	if env.OK || env.Error == nil || env.Error.Code != "E_CONFIRMATION_REQUIRED" {
		t.Fatalf("unexpected stop envelope: %+v", env)
	}
}

func TestReferenceCommandsHaveSchemasAndExamples(t *testing.T) {
	for _, cmd := range referenceCommands() {
		path, _ := cmd["path"].(string)
		schemaName, _ := cmd["output_schema"].(string)
		if path == "" || schemaName == "" {
			t.Fatalf("reference command missing path/schema: %#v", cmd)
		}
		if _, ok := referenceSchemas()[schemaName]; !ok {
			t.Fatalf("reference command %s points at missing schema %s", path, schemaName)
		}
		examples, ok := cmd["examples"].([]string)
		if !ok || len(examples) == 0 {
			t.Fatalf("reference command %s has no examples", path)
		}
	}
}

func TestUpdateSameVersionIsNotAvailable(t *testing.T) {
	result, err := buildUpdateResult(version)
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("same target version must not be update_available: %+v", result)
	}
}

func runCLIForTest(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "."}, args...)...) //nolint:gosec
	cmd.Dir = filepath.Clean("..")
	return cmd.Output()
}

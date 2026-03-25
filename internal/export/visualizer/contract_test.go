package visualizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

func TestContractFixturesMatchArchivedOutput(t *testing.T) {
	manifestBytes, runBytes := buildContractArchiveFixture(t)

	assertJSONMatchesFixture(t, manifestBytes, contractPath(t, "manifest.v1.fixture.json"))
	assertJSONMatchesFixture(t, runBytes, contractPath(t, "run.v1.fixture.json"))
}

func TestContractSchemasValidateFixturesAndArchivedOutput(t *testing.T) {
	manifestBytes, runBytes := buildContractArchiveFixture(t)
	manifestSchema := compileContractSchema(t, "manifest.v1.schema.json")
	runSchema := compileContractSchema(t, "run.v1.schema.json")

	for _, tc := range []struct {
		name       string
		schema     *jsonschema.Schema
		actualJSON []byte
		fixture    string
	}{
		{
			name:       "manifest",
			schema:     manifestSchema,
			actualJSON: manifestBytes,
			fixture:    "manifest.v1.fixture.json",
		},
		{
			name:       "run",
			schema:     runSchema,
			actualJSON: runBytes,
			fixture:    "run.v1.fixture.json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validateJSONWithSchema(t, tc.schema, tc.actualJSON)
			validateJSONWithSchema(t, tc.schema, mustReadContractFile(t, tc.fixture))
		})
	}
}

func assertJSONMatchesFixture(t *testing.T, actual []byte, fixturePath string) {
	t.Helper()

	var actualValue interface{}
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatalf("unmarshal actual JSON: %v", err)
	}

	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}

	var fixtureValue interface{}
	if err := json.Unmarshal(fixtureBytes, &fixtureValue); err != nil {
		t.Fatalf("unmarshal fixture JSON %s: %v", fixturePath, err)
	}

	if !reflect.DeepEqual(actualValue, fixtureValue) {
		t.Fatalf("fixture %s does not match archived output", fixturePath)
	}
}

func compileContractSchema(t *testing.T, file string) *jsonschema.Schema {
	t.Helper()

	schemaPath := contractPath(t, file)
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile schema %s: %v", schemaPath, err)
	}

	return schema
}

func validateJSONWithSchema(t *testing.T, schema *jsonschema.Schema, payload []byte) {
	t.Helper()

	var value interface{}
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("unmarshal payload for schema validation: %v", err)
	}

	if err := schema.Validate(value); err != nil {
		t.Fatalf("schema validation failed: %v", err)
	}
}

func mustReadContractFile(t *testing.T, file string) []byte {
	t.Helper()

	payload, err := os.ReadFile(contractPath(t, file))
	if err != nil {
		t.Fatalf("read contract file %s: %v", file, err)
	}

	return payload
}

func contractPath(t *testing.T, file string) string {
	t.Helper()

	path, err := filepath.Abs(filepath.Join("..", "..", "..", "contracts", "visualizer", file))
	if err != nil {
		t.Fatalf("resolve contract path %s: %v", file, err)
	}

	return path
}

package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateOpenAPIIsValidAndDeterministic(t *testing.T) {
	first, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOpenAPI(first); err != nil {
		t.Fatal(err)
	}
	second, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !EqualContract(first, second) {
		t.Fatal("OpenAPI generation is not deterministic")
	}
}

func TestCheckedInOpenAPIIsCurrentAndCompatible(t *testing.T) {
	root := repositoryRoot(t)
	current, err := os.ReadFile(filepath.Join(root, "contracts", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(filepath.Join(root, "contracts", "openapi.compat.json"))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !EqualContract(current, generated) {
		t.Fatal("contracts/openapi.json is stale; run `make contract-generate`")
	}
	if err := CompatibleResponseContract(baseline, current); err != nil {
		t.Fatalf("breaking client API change: %v", err)
	}
}

func TestClientContractHasExpectedPhaseOneOperationsAndNoTenantSelector(t *testing.T) {
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	for _, expected := range []string{
		"/api/dashboard",
		"/api/health-briefing",
		"/api/ai-briefing",
		"/api/readiness-history",
		"/api/energy-history",
	} {
		if _, ok := paths[expected]; !ok {
			t.Errorf("missing phase-one operation %s", expected)
		}
	}
	for path, rawPath := range paths {
		pathItem := rawPath.(map[string]any)
		get := pathItem["get"].(map[string]any)
		parameters, _ := get["parameters"].([]any)
		for _, rawParameter := range parameters {
			parameter := rawParameter.(map[string]any)
			name, _ := parameter["name"].(string)
			switch strings.ToLower(name) {
			case "tenant", "tenant_id", "schema", "schema_name":
				t.Errorf("%s exposes untrusted tenant selector %q", path, name)
			}
		}
	}
}

func TestCompatibleResponseContractRejectsBreakingChanges(t *testing.T) {
	tests := []struct {
		name       string
		oldSchema  string
		newSchema  string
		wantNeedle string
	}{
		{
			name:       "required field removed",
			oldSchema:  `{"type":"object","required":["score"],"properties":{"score":{"type":"integer"}}}`,
			newSchema:  `{"type":"object","properties":{"score":{"type":"integer"}}}`,
			wantNeedle: "required removed",
		},
		{
			name:       "property removed",
			oldSchema:  `{"type":"object","properties":{"score":{"type":"integer"}}}`,
			newSchema:  `{"type":"object","properties":{}}`,
			wantNeedle: "score removed",
		},
		{
			name:       "type changed",
			oldSchema:  `{"type":"object","properties":{"score":{"type":"integer"}}}`,
			newSchema:  `{"type":"object","properties":{"score":{"type":"string"}}}`,
			wantNeedle: "type removed",
		},
		{
			name:       "enum narrowed",
			oldSchema:  `{"type":"object","properties":{"band":{"type":"string","enum":["good","fair","low"]}}}`,
			newSchema:  `{"type":"object","properties":{"band":{"type":"string","enum":["good","low"]}}}`,
			wantNeedle: "enum removed",
		},
		{
			name:       "nullability removed",
			oldSchema:  `{"type":"object","properties":{"sleep":{"type":["object","null"]}}}`,
			newSchema:  `{"type":"object","properties":{"sleep":{"type":"object"}}}`,
			wantNeedle: "type removed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldDoc := testDocument(tc.oldSchema)
			newDoc := testDocument(tc.newSchema)
			err := CompatibleResponseContract(oldDoc, newDoc)
			if err == nil || !strings.Contains(err.Error(), tc.wantNeedle) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantNeedle)
			}
		})
	}
}

func TestCompatibleResponseContractAllowsAdditiveChanges(t *testing.T) {
	oldDoc := testDocument(`{
		"type":"object",
		"required":["score"],
		"properties":{
			"score":{"type":"integer"},
			"band":{"type":"string","enum":["good","fair"]}
		}
	}`)
	newDoc := testDocument(`{
		"type":"object",
		"required":["score"],
		"properties":{
			"score":{"type":"integer"},
			"band":{"type":"string","enum":["good","fair","low"]},
			"confidence":{"type":"string"}
		}
	}`)
	if err := CompatibleResponseContract(oldDoc, newDoc); err != nil {
		t.Fatal(err)
	}
}

func testDocument(schema string) []byte {
	return []byte(`{
		"openapi":"3.1.0",
		"components":{"schemas":{"Response":` + schema + `}}
	}`)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

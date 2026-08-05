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
		"/api/derived-metrics",
		"/api/metrics/data",
		"/api/metrics/range",
		"/api/section/{key}",
		"/api/session",
	} {
		if _, ok := paths[expected]; !ok {
			t.Errorf("missing phase-one operation %s", expected)
		}
	}
	for path, rawPath := range paths {
		pathItem := rawPath.(map[string]any)
		for method, rawOperation := range pathItem {
			if !isHTTPMethod(method) {
				continue
			}
			operation, ok := rawOperation.(map[string]any)
			if !ok {
				t.Errorf("%s %s is not an operation object", strings.ToUpper(method), path)
				continue
			}
			parameters, _ := operation["parameters"].([]any)
			for _, rawParameter := range parameters {
				parameter, ok := rawParameter.(map[string]any)
				if !ok {
					t.Errorf("%s %s contains a malformed parameter", strings.ToUpper(method), path)
					continue
				}
				name, _ := parameter["name"].(string)
				switch strings.ToLower(name) {
				case "tenant", "tenant_id", "schema", "schema_name":
					t.Errorf("%s %s exposes untrusted tenant selector %q", strings.ToUpper(method), path, name)
				}
			}
		}
	}

	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	session := schemas["SessionResponse"].(map[string]any)
	sessionProperties := session["properties"].(map[string]any)
	isAdmin := sessionProperties["is_admin"].(map[string]any)
	if isAdmin["type"] != "boolean" {
		t.Fatalf("session is_admin schema = %#v, want boolean", isAdmin)
	}

	derived := paths["/api/derived-metrics"].(map[string]any)["get"].(map[string]any)
	responses := derived["responses"].(map[string]any)
	badRequest, ok := responses["400"].(map[string]any)
	if !ok {
		t.Fatal("derived metrics operation does not document a 400 response")
	}
	badRequestSchema := badRequest["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if badRequestSchema["type"] != "object" {
		t.Fatalf("derived metrics 400 schema=%#v, want JSON object", badRequestSchema)
	}
	parameters := derived["parameters"].([]any)
	metricParameter := parameters[0].(map[string]any)
	if !metricParameter["required"].(bool) || strings.Contains(metricParameter["description"].(string), "falls back") {
		t.Fatalf("derived metric parameter contract=%#v", metricParameter)
	}
	if strings.Contains(string(raw), `"value_json": true`) {
		t.Fatal("OpenAPI contains a boolean schema that compatibility tooling cannot parse")
	}
}

func TestClientContractModelsColdStartAndDiscriminatedEnergyResponses(t *testing.T) {
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDocument(t, raw)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	briefing := schemas["HealthBriefingResponse"].(map[string]any)
	properties := briefing["properties"].(map[string]any)
	for _, field := range []string{"overall", "readiness_band", "readiness_today_band"} {
		enum := valueSet(properties[field].(map[string]any)["enum"])
		empty, _ := json.Marshal("")
		if _, ok := enum[string(empty)]; !ok {
			t.Errorf("%s does not model the empty fresh-tenant value", field)
		}
	}

	paths := doc["paths"].(map[string]any)
	energy := paths["/api/energy-history"].(map[string]any)["get"].(map[string]any)
	energySchema, err := operationResponseSchema(energy, "200")
	if err != nil {
		t.Fatal(err)
	}
	discriminator := energySchema["discriminator"].(map[string]any)
	mapping := discriminator["mapping"].(map[string]any)
	if mapping["day"] != "#/components/schemas/EnergyHistoryDayResponse" ||
		mapping["hour"] != "#/components/schemas/EnergyHistoryHourResponse" {
		t.Fatalf("energy discriminator mapping = %#v", mapping)
	}

	hour := schemas["EnergyHistoryHourResponse"].(map[string]any)
	pointsArray := hour["properties"].(map[string]any)["points"].(map[string]any)
	points := findObjectSchema(pointsArray["items"].(map[string]any))
	components := points["properties"].(map[string]any)["components"].(map[string]any)
	if components["type"] != "object" {
		t.Fatalf("hourly energy components schema = %#v, want object", components)
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

	widenedOld := testDocument(`{"type":"object","properties":{"sleep":{"type":"object"}}}`)
	widenedNew := testDocument(`{"type":"object","properties":{"sleep":{"type":["object","null"]}}}`)
	if err := CompatibleResponseContract(widenedOld, widenedNew); err != nil {
		t.Fatalf("adding nullability must be additive: %v", err)
	}
}

func TestCompatibleResponseContractRejectsOperationBreaks(t *testing.T) {
	baseline, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		mutate     func(map[string]any)
		wantNeedle string
	}{
		{
			name: "path removed",
			mutate: func(doc map[string]any) {
				delete(doc["paths"].(map[string]any), "/api/dashboard")
			},
			wantNeedle: "path /api/dashboard removed",
		},
		{
			name: "operation removed",
			mutate: func(doc map[string]any) {
				path := doc["paths"].(map[string]any)["/api/dashboard"].(map[string]any)
				delete(path, "get")
			},
			wantNeedle: "operation GET /api/dashboard removed",
		},
		{
			name: "success response removed",
			mutate: func(doc map[string]any) {
				paths := doc["paths"].(map[string]any)
				dashboard := paths["/api/dashboard"].(map[string]any)["get"].(map[string]any)
				delete(dashboard["responses"].(map[string]any), "200")
			},
			wantNeedle: "response 200 missing",
		},
		{
			name: "success response ref changed",
			mutate: func(doc map[string]any) {
				paths := doc["paths"].(map[string]any)
				dashboard := paths["/api/dashboard"].(map[string]any)["get"].(map[string]any)
				schema, err := operationResponseSchema(dashboard, "200")
				if err != nil {
					t.Fatal(err)
				}
				schema["$ref"] = "#/components/schemas/AIBriefingResponse"
			},
			wantNeedle: "responses.200.refs removed",
		},
		{
			name: "parameter enum narrowed",
			mutate: func(doc map[string]any) {
				paths := doc["paths"].(map[string]any)
				briefing := paths["/api/health-briefing"].(map[string]any)["get"].(map[string]any)
				parameter := briefing["parameters"].([]any)[0].(map[string]any)
				parameter["schema"].(map[string]any)["enum"] = []any{"en"}
			},
			wantNeedle: "enum removed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := decodeDocument(t, baseline)
			tc.mutate(current)
			currentRaw, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			err = CompatibleResponseContract(baseline, currentRaw)
			if err == nil || !strings.Contains(err.Error(), tc.wantNeedle) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantNeedle)
			}
		})
	}
}

func TestEnumHelpersFailOnSchemaDrift(t *testing.T) {
	if err := setPropertyEnum(map[string]any{}, []string{"missing"}, "value"); err == nil {
		t.Fatal("setPropertyEnum accepted a missing path")
	}
	if err := setNestedPropertyEnum(map[string]any{}, "missing", "child", "value"); err == nil {
		t.Fatal("setNestedPropertyEnum accepted a missing path")
	}
}

func testDocument(schema string) []byte {
	return []byte(`{
		"openapi":"3.1.0",
		"paths":{
			"/test":{
				"get":{
					"responses":{
						"200":{
							"content":{
								"application/json":{"schema":{"$ref":"#/components/schemas/Response"}}
							}
						}
					}
				}
			}
		},
		"components":{"schemas":{"Response":` + schema + `}}
	}`)
}

func decodeDocument(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func isHTTPMethod(value string) bool {
	switch value {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	default:
		return false
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

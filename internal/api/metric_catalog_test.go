package api

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestMetricCatalogOpenAPIHasArrayShapeAndOptionalMetadata(t *testing.T) {
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	if paths["/api/metrics"] == nil {
		t.Fatal("catalogue endpoint missing from shared contract")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	catalog := schemas["MetricCatalogResponse"].(map[string]any)
	types, _ := catalog["type"].([]any)
	if catalog["type"] != "array" && !slices.Contains(types, any("array")) {
		t.Fatalf("catalogue shape=%v, want array", catalog["type"])
	}
	item := catalog["items"].(map[string]any)
	if ref, ok := item["$ref"].(string); ok {
		const prefix = "#/components/schemas/"
		item = schemas[ref[len(prefix):]].(map[string]any)
	}
	properties := item["properties"].(map[string]any)
	if properties["unit_status"] == nil || properties["variant_label"] == nil {
		t.Fatal("catalogue metadata missing")
	}
	for _, required := range item["required"].([]any) {
		if required == "unit_status" || required == "variant_label" {
			t.Fatal("new metadata must remain optional")
		}
	}
}

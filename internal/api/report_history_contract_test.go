package api

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"health-receiver/internal/storage"
)

func TestReportHistoryUUIDSchemaMatchesSerializedResponse(t *testing.T) {
	id := uuid.New()
	body, err := json.Marshal(storage.ReportHistory{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err = json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["id"] != id.String() {
		t.Fatalf("UUID wire value=%#v", decoded["id"])
	}
	schema, err := reflectedSchema(storage.ReportHistory{})
	if err != nil {
		t.Fatal(err)
	}
	property := schema["properties"].(map[string]any)["id"].(map[string]any)
	if property["type"] != "string" || property["format"] != "uuid" {
		t.Fatalf("UUID schema disagrees with serialized string: %#v", property)
	}
}

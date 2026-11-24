package storage

import (
	"os"
	"strings"
	"testing"
)

func TestHDBTServingPinsGoAggregateIdentity(t *testing.T) {
	const expected = "sha256:656754be9a0accb75edee45ec48b80e8709522e267edb93de4d35b51ff7c4ed2"
	if checksum := AggregateContractChecksum(); checksum != expected || AggregateContractVersion != "8" {
		t.Fatalf("Go writer identity changed to %s / %s", AggregateContractVersion, checksum)
	}
}

func TestHDBTWorkerPinsTenantContract(t *testing.T) {
	script, err := os.ReadFile("../../experiments/dbt_metrics/scripts/tenant_aggregate_worker.py")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `SCHEMA_CHECKSUM = "`+SchemaContractChecksum()+`"`) {
		t.Fatal("worker must require the backend tenant schema contract checksum")
	}
}

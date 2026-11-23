package storage

import (
	"os"
	"strings"
	"testing"
)

func TestHDBTServingPinsGoAggregateIdentity(t *testing.T) {
	const expected = "sha256:1a4b28d778d80f4d4cbf9196a823d52d12e605c487c37abddc9b644a4ef38473"
	if checksum := AggregateContractChecksum(); checksum != expected || AggregateContractVersion != "7" {
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

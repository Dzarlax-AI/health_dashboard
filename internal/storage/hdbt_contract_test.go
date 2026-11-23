package storage

import (
	"os"
	"strings"
	"testing"
)

func TestHDBTServingPreservesGoAggregateIdentity(t *testing.T) {
	const deployed = "sha256:537bc145ef80ef9cc6d987b3e4dc9057a0759a7879e05996ca41e6928470799b"
	if checksum := AggregateContractChecksum(); checksum != deployed || AggregateContractVersion != "6" {
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

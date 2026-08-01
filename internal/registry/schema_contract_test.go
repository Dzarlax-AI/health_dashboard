package registry

import "testing"

func TestEqualRegistryColumnsIgnoresPhysicalOrder(t *testing.T) {
	expected := []string{
		"tenant_id:uuid:not-null",
		"provisioning_state:text:not-null",
		"schema_contract_version:integer:nullable",
	}
	productionOrder := []string{
		"tenant_id:uuid:not-null",
		"schema_contract_version:integer:nullable",
		"provisioning_state:text:not-null",
	}

	if !equalRegistryColumns(productionOrder, expected) {
		t.Fatal("equivalent registry columns must not drift solely because additive migrations used a different physical order")
	}
}

func TestEqualRegistryColumnsStillRejectsShapeDrift(t *testing.T) {
	expected := []string{"tenant_id:uuid:not-null", "provisioning_state:text:not-null"}
	wrongNullability := []string{"tenant_id:uuid:nullable", "provisioning_state:text:not-null"}

	if equalRegistryColumns(wrongNullability, expected) {
		t.Fatal("registry column type and nullability remain part of the contract")
	}
}

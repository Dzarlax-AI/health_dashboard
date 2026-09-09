package storage

import "testing"

func TestPlanInputsHashBindsPlanToDecision(t *testing.T) {
	hash := PlanInputsHash("decision-a", "content-hash")
	if !PlanMatchesDecision(hash, "decision-a") {
		t.Fatal("current decision did not match its plan metadata")
	}
	if PlanMatchesDecision(hash, "decision-b") {
		t.Fatal("old plan matched a changed decision")
	}
	if PlanMatchesDecision("legacy", "decision-a") {
		t.Fatal("legacy cache entry must not be presented as a fresh plan")
	}
}

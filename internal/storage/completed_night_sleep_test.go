package storage

import "testing"

func TestNightSleepSourcePriorityMatchesSleepAggregationPolicy(t *testing.T) {
	if nightSleepSourceRank("Apple Watch Ultra") >= nightSleepSourceRank("iPhone") {
		t.Fatal("Apple Watch must outrank iPhone")
	}
	if nightSleepSourceRank("iPhone") >= nightSleepSourceRank("RingConn") {
		t.Fatal("iPhone must outrank RingConn")
	}
	if nightSleepSourceRank("RingConn") >= nightSleepSourceRank("Other tracker") {
		t.Fatal("RingConn must outrank an unclassified tracker")
	}
}

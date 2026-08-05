package storage

import "testing"

func TestShouldBackoffAIRegenDoesNotTreatFullyCachedRunAsFailure(t *testing.T) {
	tests := []struct {
		name   string
		saved  int
		failed int
		want   bool
	}{
		{name: "fully cached", saved: 0, failed: 0, want: false},
		{name: "total provider failure", saved: 0, failed: 1, want: true},
		{name: "partial success", saved: 1, failed: 1, want: false},
		{name: "generated successfully", saved: 1, failed: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldBackoffAIRegen(tt.saved, tt.failed); got != tt.want {
				t.Fatalf("shouldBackoffAIRegen(%d, %d) = %v, want %v", tt.saved, tt.failed, got, tt.want)
			}
		})
	}
}

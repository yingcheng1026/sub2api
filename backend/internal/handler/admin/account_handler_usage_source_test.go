package admin

import "testing"

func TestNormalizeAccountUsageSourceDefaultsToPassive(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "missing", source: "", want: "passive"},
		{name: "active", source: "active", want: "active"},
		{name: "passive", source: "passive", want: "passive"},
		{name: "unknown", source: "unexpected", want: "passive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAccountUsageSource(tt.source); got != tt.want {
				t.Fatalf("normalizeAccountUsageSource(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

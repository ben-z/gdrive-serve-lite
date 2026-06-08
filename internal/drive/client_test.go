package drive

import "testing"

func TestQuoteEscapesDriveQueryString(t *testing.T) {
	got := quote(`quinn's paper\essay`)
	want := `'quinn\'s paper\\essay'`
	if got != want {
		t.Fatalf("quote() = %q, want %q", got, want)
	}
}

func TestNormalizePageSize(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{0, 1000},
		{-1, 1000},
		{1, 1},
		{100, 100},
		{5000, 1000},
	}
	for _, tt := range tests {
		if got := normalizePageSize(tt.in); got != tt.want {
			t.Fatalf("normalizePageSize(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

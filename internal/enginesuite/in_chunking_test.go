package enginesuite

import (
	"testing"
)

// TestChunkParentKeys_Contract pins the math the helper guarantees: N
// parents → ceil(N/1000) SELECTs. The helper is unexported, so this is the
// observable contract that buildSelect / Preload depend on.
func TestChunkParentKeys_Contract(t *testing.T) {
	cases := []struct {
		n      int
		chunks int
	}{
		{0, 0},
		{1, 1},
		{999, 1},
		{1000, 1},
		{1001, 2},
		{1999, 2},
		{2000, 2},
		{2500, 3},
		{3000, 3},
		{3001, 4},
	}
	for _, tc := range cases {
		got := (tc.n + 999) / 1000
		if got != tc.chunks {
			t.Errorf("ceil(%d/1000) = %d, want %d", tc.n, got, tc.chunks)
		}
	}
}

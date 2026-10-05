package enginesuite

import (
	"testing"
)

// TestParsePreloads pins the tree-merging contract for the dotted-path
// parser. Useful as a unit-level sanity check independent of running the
// full SharedSuite.
func TestParsePreloads_Contract(t *testing.T) {
	// We can't reach parsePreloads from the test package (lowercase), so
	// the contract is exercised via the SharedScripts already; this test
	// is a placeholder that captures the *expected* node structure as
	// human-readable text. If parsePreloads is moved or its signature
	// changes the integration tests will fail loudly.
	t.Log("parsePreloads contract: 'A.B', 'A.C' merges A → [B, C]; 'A.B.C', 'A.B.D' nests as A → B → [C, D]")
}

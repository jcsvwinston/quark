package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/acceptance/control"
)

// TestReadmeFiguresMatchManifest holds the figures README.md publishes about
// the denominator — symbols, packages, allowlisted, in-scope and the share
// the strict gate covers — to the two JSON files they describe. The README
// carried a hand-written 720 for a year while the manifest said 928; a
// number nobody counts drifts, so this test counts it: `make check`
// regenerates both files and then runs this, so a regeneration that moves a
// figure fails until the README says the new one.
func TestReadmeFiguresMatchManifest(t *testing.T) {
	m, err := control.LoadManifest("apisurface.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := control.LoadAllowlist("allowlist.json")
	if err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]bool{}
	for _, s := range m.Symbols {
		pkgs[s.Pkg] = true
	}
	symbols, packages, allowlisted := len(m.Symbols), len(pkgs), len(a.Reasons)
	inScope := symbols - allowlisted
	share := int(float64(inScope)*100/float64(symbols) + 0.5)

	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)
	want := []string{
		fmt.Sprintf("%d símbolos en %d paquetes", symbols, packages),
		fmt.Sprintf("%d símbolos en el manifiesto", symbols),
		fmt.Sprintf("%d en `allowlist.json`", allowlisted),
		fmt.Sprintf("%d in-scope", inScope),
		fmt.Sprintf("el gate estricto cubre el %d %%", share),
	}
	var missing []string
	for _, w := range want {
		if !strings.Contains(text, w) {
			missing = append(missing, w)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("README.md does not carry the measured figure(s) %q — apisurface.json has %d symbols in %d packages, allowlist.json %d reasons (%d in-scope, %d %%); update the sentences that quote this test",
			missing, symbols, packages, allowlisted, inScope, share)
	}
	t.Logf("denominator: %d symbols, %d packages, %d allowlisted, %d in-scope (%d %% under the strict gate)", symbols, packages, allowlisted, inScope, share)
}

// Command gen-allowlist genera allowlist.json: la decisión de DENOMINADOR del
// gate de cobertura (S7-coverage). El gate cuenta como "missing" todo símbolo
// del manifiesto (apisurface.json) que ningún exerciser invoca. Dos categorías
// no son invocables por un gate basado en invocación y se justifican aquí en
// bloque, dejando que el gate mida la superficie CALLABLE del API público:
//
//   - Métodos de dialecto (`(*XxxDialect).*`): plumbing interno, ejercido
//     transitivamente por CADA query (Quote/Placeholder/Returning/…) y
//     unit-tested por motor en dialect_test.go. No son un entry point público
//     directo.
//   - Tipos / consts / vars: no se "invocan" — su comportamiento se ejerce a
//     través de sus métodos (contados aparte) y de las funcs/métodos que los
//     consumen.
//
// Las excepciones manuales (p.ej. el alias deprecado RowLevelSecurity) viven en
// manualReasons y se preservan en cada regeneración. El fichero es
// determinista (claves ordenadas, sin timestamp) para que un símbolo público
// nuevo produzca un diff limpio y CI pueda exigir regenerar.
//
//	go run ./examples/superapp/cmd/gen-allowlist            # escribe examples/superapp/allowlist.json
//	go run ./examples/superapp/cmd/gen-allowlist -out=/tmp/x.json
//
//go:generate go run . -out=../../allowlist.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/jcsvwinston/quark/examples/superapp/control"
)

// dialectMethod casa un método cuyo receptor es un tipo *XxxDialect (las
// implementaciones concretas, p.ej. (*PostgresDialect).Quote) o la interfaz
// Dialect misma ((Dialect).Quote) — \w* cubre el receptor vacío de la interfaz.
var dialectMethod = regexp.MustCompile(`^\(\*?\w*Dialect\)\.`)

// manualReasons son las excepciones curadas a mano (no derivables del kind):
// símbolos callable que conscientemente NO se ejercen. Se preservan al regenerar.
var manualReasons = map[string]string{
	"github.com/jcsvwinston/quark.RowLevelSecurity": "alias deprecado de RowLevelSecurityClient (desde v1.0, se retira en v2.0); el exerciser cubre RowLevelSecurityClient en su lugar",
}

const (
	reasonDialect = "método de dialecto interno: ejercido transitivamente por cada query y unit-tested por motor en dialect_test.go; no es un entry point público directo (denominador S7-coverage)"
	reasonType    = "tipo: no invocable por el gate; su comportamiento se ejerce vía sus métodos (contados aparte) y las funcs/métodos que lo consumen (denominador S7-coverage)"
	reasonConst   = "const: no invocable (se referencia como valor, nunca se llama) (denominador S7-coverage)"
	reasonVar     = "package var: no invocable (denominador S7-coverage)"
)

func main() {
	manifestPath := flag.String("manifest", "examples/superapp/apisurface.json", "ruta al apisurface.json (denominador)")
	out := flag.String("out", "examples/superapp/allowlist.json", "ruta de salida")
	flag.Parse()

	m, err := control.LoadManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-allowlist:", err)
		os.Exit(1)
	}

	reasons := map[string]string{}
	for _, s := range m.Symbols {
		switch {
		case s.Kind == "type":
			reasons[s.Key()] = reasonType
		case s.Kind == "const":
			reasons[s.Key()] = reasonConst
		case s.Kind == "var":
			reasons[s.Key()] = reasonVar
		case s.Kind == "method" && dialectMethod.MatchString(s.Name):
			reasons[s.Key()] = reasonDialect
		}
	}
	// Las excepciones manuales ganan (preservan su razón específica).
	for k, v := range manualReasons {
		reasons[k] = v
	}

	a := control.Allowlist{Reasons: reasons}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-allowlist:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen-allowlist:", err)
		os.Exit(1)
	}
	fmt.Printf("gen-allowlist: %d símbolos allowlisted → %s\n", len(reasons), *out)
}

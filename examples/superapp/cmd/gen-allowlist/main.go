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

// interfaceTypes son los tipos INTERFAZ públicos (excluidos Dialect/
// SavepointDialect, que ya cubre dialectMethod). Sus métodos son CONTRATOS
// ejercidos vía las implementaciones — los métodos concretos de cada impl se
// cuentan aparte (p.ej. (*Store).Get cubre el contrato (CacheStore).Get; los
// modelos del dominio cubren los hooks). No son entry points invocables
// directos, así que entran al denominador como allowlist (misma decisión que
// la interfaz Dialect en PR #1). Mantener en sync con `type X interface` del
// API público si se añade una interfaz nueva.
var interfaceTypes = map[string]bool{
	"EventListener": true, "Event": true, "EventBus": true, "QueryObserver": true,
	"Middleware": true, "Executor": true, "DBConn": true, "DBConnector": true,
	"CacheStore": true, "CacheLocker": true, "ClientProvider": true, "ColumnTypeMapper": true,
	"SchemaIntrospector": true, "MigrationLock": true, "MigrationLocker": true,
	"Expr": true, "Operation": true, "Result": true, "Row": true, "PoolOption": true,
	"ShardKeyer":      true,
	"AfterCreateHook": true, "AfterUpdateHook": true, "AfterDeleteHook": true, "AfterFindHook": true,
	"BeforeCreateHook": true, "BeforeUpdateHook": true, "BeforeDeleteHook": true, "BeforeFindHook": true,
}

// recvType extrae el nombre del tipo receptor de un símbolo método:
// "(*Foo[T]).Bar" → "Foo", "(Iface).M" → "Iface"; "" si no es método.
var recvRe = regexp.MustCompile(`^\(\*?([A-Za-z_]\w*)`)

func recvType(name string) string {
	if m := recvRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// manualReasons son las excepciones curadas a mano (no derivables del kind):
// símbolos callable que conscientemente NO se ejercen. Se preservan al regenerar.
var manualReasons = map[string]string{
	"github.com/jcsvwinston/quark.RowLevelSecurity": "alias deprecado de RowLevelSecurityClient (desde v1.0, se retira en v2.0); el exerciser cubre RowLevelSecurityClient en su lugar",

	// Stored routines/procedures: ejecutar uno necesita un fixture DB-side por
	// motor (TVF/proc); no portable en el arnés in-process. La construcción del
	// SQL está cubierta a nivel dialecto (BuildRoutineQuery/BuildProcedureCall).
	"github.com/jcsvwinston/quark.NewRoutine":           reasonRoutine,
	"github.com/jcsvwinston/quark.Call":                 reasonRoutine,
	"github.com/jcsvwinston/quark.(*Routine[T]).First":  reasonRoutine,
	"github.com/jcsvwinston/quark.(*Routine[T]).List":   reasonRoutine,
	"github.com/jcsvwinston/quark.(*Routine[T]).Scalar": reasonRoutine,

	// cache/redis: necesita un Redis vivo; se ejerce contra redis:7 real en
	// recorder/infra_test.go (tag superapp_infra), fuera del run por-motor del gate.
	"github.com/jcsvwinston/quark/cache/redis.New":                     reasonRedis,
	"github.com/jcsvwinston/quark/cache/redis.(*Store).Get":            reasonRedis,
	"github.com/jcsvwinston/quark/cache/redis.(*Store).Set":            reasonRedis,
	"github.com/jcsvwinston/quark/cache/redis.(*Store).Delete":         reasonRedis,
	"github.com/jcsvwinston/quark/cache/redis.(*Store).InvalidateTags": reasonRedis,
	"github.com/jcsvwinston/quark/cache/redis.(*Store).AcquireLock":    reasonRedis,
	"github.com/jcsvwinston/quark/cache/redis.(*Store).Ping":           reasonRedis,

	// Entrypoints CLI/instalador: necesitan args + sesión DB viva (y PG para
	// RLS). Cubiertos por el exerciser cli (cmd/quark) y los tests de tenant;
	// ParseAction/DefaultInstallOptions (puros) sí se ejercen en surface.
	"github.com/jcsvwinston/quark/quarktenant.Run":                reasonCLIRun,
	"github.com/jcsvwinston/quark/quarktenant.RunWithIO":          reasonCLIRun,
	"github.com/jcsvwinston/quark/quarktenant.InstallRLSPolicies": reasonCLIRun,
	"github.com/jcsvwinston/quark/quarkmigrate.Run":               reasonCLIRun,
	"github.com/jcsvwinston/quark/quarkmigrate.RunWithOutput":     reasonCLIRun,
}

const (
	reasonRoutine = "stored routine/proc: ejecutar necesita un fixture DB-side por motor; no portable in-process (el SQL se cubre a nivel dialecto) (denominador S7-coverage)"
	reasonRedis   = "necesita un Redis vivo; ejercido contra redis real en recorder/infra_test.go (tag superapp_infra), fuera del run por-motor del gate (denominador S7-coverage)"
	reasonCLIRun  = "entrypoint CLI/instalador: necesita args + sesión DB viva (PG para RLS); cubierto por el exerciser cli + tests de tenant; ParseAction/DefaultInstallOptions sí se ejercen (denominador S7-coverage)"
)

const (
	reasonDialect = "método de dialecto interno: ejercido transitivamente por cada query y unit-tested por motor en dialect_test.go; no es un entry point público directo (denominador S7-coverage)"
	reasonType    = "tipo: no invocable por el gate; su comportamiento se ejerce vía sus métodos (contados aparte) y las funcs/métodos que lo consumen (denominador S7-coverage)"
	reasonConst   = "const: no invocable (se referencia como valor, nunca se llama) (denominador S7-coverage)"
	reasonVar     = "package var: no invocable (denominador S7-coverage)"
	reasonIface   = "método de interfaz: contrato ejercido vía las implementaciones (sus métodos concretos se cuentan aparte), no un entry point invocable directo (denominador S7-coverage)"
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
		case s.Kind == "method" && interfaceTypes[recvType(s.Name)]:
			reasons[s.Key()] = reasonIface
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

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
	// quarkdriver.Listener es el tipo real detrás del alias EventListener.
	"Listener":   true, "IdentifierValidator": true,
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
	// QK-6/QK-8 (auditoría de madurez 2026-09-03): opciones del migrador y
	// contrato público del listener. Los ejerce el paquete migrate y el
	// módulo de driver, no el flujo del superapp.
	"github.com/jcsvwinston/quark/migrate.WithoutLock":                     "opción del migrador (QK-6): la ejerce migrate/a1_lock_tx_test.go; el superapp corre en un solo proceso y migra con el lock por defecto (denominador S7-coverage)",
	"github.com/jcsvwinston/quark/migrate.WithLockTimeout":                 "opción del migrador (QK-6): documentada y probada en migrate/; el superapp no la varía (denominador S7-coverage)",
	"github.com/jcsvwinston/quark/migrate.WithLockName":                    "opción del migrador (QK-6): documentada y probada en migrate/; el superapp no la varía (denominador S7-coverage)",
	"github.com/jcsvwinston/quark/migrate.WithLogger":                      "opción del migrador (QK-6): la ejerce migrate/a1_lock_tx_test.go (denominador S7-coverage)",
	"github.com/jcsvwinston/quark.(*Client).Logger":                        "accesor del logger del cliente (QK-6): lo consume el migrador; no es un entry point de aplicación (denominador S7-coverage)",
	"github.com/jcsvwinston/quark/quarkdriver.RegisterListenerFactory":     "contrato de módulo de driver (QK-8): lo llama el init() del módulo de PostgreSQL al adoptar el contrato público; RegisterListener lo adapta mientras tanto (denominador S7-coverage)",
	"github.com/jcsvwinston/quark/quarkdriver.MustRegisterListenerFactory": "contrato de módulo de driver (QK-8): init() del módulo, no código de aplicación (denominador S7-coverage)",
	"github.com/jcsvwinston/quark/quarkdriver.LookupListenerFactory":       "contrato de módulo de driver (QK-8): lo llama el ORM al abrir un listener (events.go); no es un entry point de aplicación (denominador S7-coverage)",
	"github.com/jcsvwinston/quark.RowLevelSecurity":                        "alias deprecado de RowLevelSecurityClient (desde v1.0, se retira en v2.0); el exerciser cubre RowLevelSecurityClient en su lugar",

	// Contrato de los módulos de driver (ADR-0023). Es superficie de AUTOR DE
	// DRIVER, no de aplicación: lo llama el init() de cada módulo, no el
	// código que consulta una base de datos. Ejercerlo desde el superapp
	// probaría que la función existe, no que el driver clasifica bien — que es
	// lo que sí prueba la suite de conformidad de cada módulo
	// (quarkdriver/drivertest), con errores REALES del motor.
	"github.com/jcsvwinston/quark/quarkdriver.Register":             reasonDriverContract,
	"github.com/jcsvwinston/quark/quarkdriver.MustRegister":         reasonDriverContract,
	"github.com/jcsvwinston/quark/quarkdriver.Classifiers":          reasonDriverContract,
	"github.com/jcsvwinston/quark/quarkdriver.RegisteredEngines":    reasonDriverContract,
	"github.com/jcsvwinston/quark/quarkdriver.HasEngine":            reasonDriverContract,
	"github.com/jcsvwinston/quark/quarkdriver.RegisterListener":     reasonDriverContract,
	"github.com/jcsvwinston/quark/quarkdriver.MustRegisterListener": reasonDriverContract,
	"github.com/jcsvwinston/quark/quarkdriver.LookupListener":       reasonDriverContract,

	// El kit de conformidad recibe *testing.T: sólo es invocable desde un
	// test, y lo ejecutan los cinco módulos de driver en los suyos.
	"github.com/jcsvwinston/quark/quarkdriver/drivertest.Verify": reasonTestKit,

	// La pista del driver ausente sólo se puede ejercer en un binario que NO
	// enlace ese driver, y el superapp los enlaza los seis por definición:
	// llamarla aquí probaría que la función devuelve texto, no que el arranque
	// dice qué importar. Eso lo cubren sus tests de mesa en quarkdriver.
	"github.com/jcsvwinston/quark/quarkdriver.MissingDriverHint": reasonMissingDriver,
	"github.com/jcsvwinston/quark/quarkdriver.IsRegistered":      reasonMissingDriver,

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
	"github.com/jcsvwinston/quark/quarktenant.VerifyRLSPolicies":  reasonCLIRun,

	// El kit de testing recibe testing.TB: es INVOCABLE solo desde un test,
	// nunca desde el superapp (que es un binario). Su cobertura por
	// ejecución vive en quarktest/quarktest_test.go (SQLite+Migrate+Tx con
	// rollback verificado); listarlo aquí evita fingir una llamada.
	"github.com/jcsvwinston/quark/quarktest.SQLite":           reasonTestKit,
	"github.com/jcsvwinston/quark/quarktest.Migrate":          reasonTestKit,
	"github.com/jcsvwinston/quark/quarktest.Tx":               reasonTestKit,
	"github.com/jcsvwinston/quark/quarkmigrate.Run":           reasonCLIRun,
	"github.com/jcsvwinston/quark/quarkmigrate.RunWithOutput": reasonCLIRun,

	// Registro de seeders (simétrico a migrate.Register): lo llama el init()
	// de los ficheros de seeder generados y lo consume `quark seed run`
	// (exerciser cli), nunca el superapp — que no compila seeders de usuario.
	"github.com/jcsvwinston/quark/seed.Register": reasonSeedRegistry,
	"github.com/jcsvwinston/quark/seed.Get":      reasonSeedRegistry,
	"github.com/jcsvwinston/quark/seed.Names":    reasonSeedRegistry,
	"github.com/jcsvwinston/quark/seed.Count":    reasonSeedRegistry,
	"github.com/jcsvwinston/quark/seed.Reset":    reasonSeedRegistry,
}

const (
	reasonRoutine        = "stored routine/proc: ejecutar necesita un fixture DB-side por motor; no portable in-process (el SQL se cubre a nivel dialecto) (denominador S7-coverage)"
	reasonRedis          = "necesita un Redis vivo; ejercido contra redis real en recorder/infra_test.go (tag superapp_infra), fuera del run por-motor del gate (denominador S7-coverage)"
	reasonCLIRun         = "entrypoint CLI/instalador/verificador: necesita args + sesión DB viva (PG para RLS); cubierto por el exerciser cli + tests de tenant (install y verify con PG real); ParseAction/DefaultInstallOptions sí se ejercen (denominador S7-coverage)"
	reasonMissingDriver  = "pista de driver ausente: sólo observable en un binario que NO enlace ese driver, y el superapp enlaza los seis; cubierta por los tests de mesa de quarkdriver (denominador S7-coverage)"
	reasonDriverContract = "contrato de módulo de driver (ADR-0023): lo llama el init() de cada módulo, no el código de una aplicación; su comportamiento lo prueba la suite de conformidad de cada driver con errores REALES del motor (denominador S7-coverage)"
	reasonTestKit        = "helper de kit de testing (recibe testing.TB): solo invocable desde un test; cubierto por ejecución en quarktest/quarktest_test.go (denominador S7-coverage)"

	reasonSeedRegistry = "registro de seeders simétrico a migrate.Register: lo llama el init() de los ficheros de seeder generados y lo consume `quark seed run` (exerciser cli); el superapp no compila seeders de usuario (denominador S7-coverage)"
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

package main

import (
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestExtractKnownSymbols ancla la extracción: un denominador incorrecto
// invalida el gate entero, así que verificamos que símbolos representativos
// aparecen con el kind correcto y que los no-exportados / métodos de alias NO.
// Carga go/packages (lento); se salta bajo -short.
func TestExtractKnownSymbols(t *testing.T) {
	if testing.Short() {
		t.Skip("carga go/packages sobre todo quark; lento")
	}
	pkgs, err := packages.Load(&packages.Config{Mode: loadMode},
		"github.com/jcsvwinston/quark", "github.com/jcsvwinston/quark/quarkdriver")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("%d errores de carga", n)
	}

	kind := map[string]string{}
	alias := map[string]string{}
	sig := map[string]string{}
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		for _, s := range extract(p) {
			kind[s.Key()] = s.Kind
			alias[s.Key()] = s.AliasOf
			sig[s.Key()] = s.Sig
		}
	}

	const root = "github.com/jcsvwinston/quark."
	const leaf = "github.com/jcsvwinston/quark/quarkdriver."
	mustHave := map[string]string{
		// El contrato del dialecto vive en quarkdriver (ADR-0026): sus
		// métodos se listan allí; quark conserva el nombre como alias.
		leaf + "(Dialect).Quote":         "method",
		leaf + "(LockOptions).IsZero":    "method",
		leaf + "RegisterDialect":         "func",
		root + "Dialect":                 "type",
		root + "RegisterDialect":         "func",
		root + "(*Query[T]).List":        "method", // método de tipo genérico
		root + "(*Query[T]).Find":        "method",
		root + "(*Query[T]).CreateBatch": "method",
		root + "For":                     "func", // func de paquete (genérica)
		root + "New":                     "func",
		root + "RowLevelSecurity":        "const", // alias deprecado (allowlist)
		root + "Nullable":                "type",  // alias genérico
	}
	for key, want := range mustHave {
		got, ok := kind[key]
		if !ok {
			t.Errorf("falta símbolo esperado: %s", key)
			continue
		}
		if got != want {
			t.Errorf("%s: kind=%q, quería %q", key, got, want)
		}
	}

	mustNotHave := []string{
		root + "cloneForGroup",        // no exportado
		root + "(Nullable).Scan",      // alias genérico → sin métodos de sql.Null
		root + "(Dialect).Quote",      // alias → sus métodos van bajo quarkdriver
		root + "(LockOptions).IsZero", // ídem
	}
	for _, key := range mustNotHave {
		if _, ok := kind[key]; ok {
			t.Errorf("símbolo que NO debía estar en el denominador: %s", key)
		}
	}

	// Un alias apunta al tipo nombrado que lo declara; un tipo que no es
	// alias no apunta a nada. Es lo que une el nombre que se queda con los
	// métodos que se fueron.
	for key, want := range map[string]string{
		root + "Dialect":     leaf + "Dialect",
		root + "Schema":      leaf + "Schema",
		root + "Executor":    leaf + "Executor",
		root + "Nullable":    "database/sql.Null",
		root + "Client":      "",
		leaf + "Dialect":     "",
		leaf + "LockOptions": "",
	} {
		if got := alias[key]; got != want {
			t.Errorf("%s: alias_of=%q, quería %q", key, got, want)
		}
	}

	// The signature is what the file freezes (A11 Q6, CON-02): a parameter
	// that changes type moves the line, a renamed one does not. An alias of
	// a type an in-scope package declares records nothing — alias_of says
	// where — and an alias of an internal type records its whole shape,
	// because nothing else lists it.
	for key, want := range map[string]string{
		leaf + "(Dialect).UpsertSQL":   "func([]string, []string, int) string",
		leaf + "(Dialect).LockSuffix":  "func(LockOptions) (string, string, error)",
		leaf + "(SQLStater).SQLState":  "func() string",
		leaf + "Classifier":            "struct{UniqueViolation func(error) bool; Deadlock func(error) bool; TransientConn func(error) bool}",
		leaf + "Dialect":               "interface",
		leaf + "ErrUnsupportedFeature": "error",
		root + "Dialect":               "",
		root + "For":                   "func[T any](context.Context, ClientProvider) *Query[T]",
		root + "New":                   "func(string, string, ...any) (*Client, error)",
		root + "(*Query[T]).Where":     "func(string, string, any) *Query[T]",
		root + "(Validator).Validate":  "func(context.Context) error",
		root + "Scope":                 "[T any] func(*Query[T]) *Query[T]",
		root + "Nullable":              "[T any] = sql.Null[T]",
		root + "TableNamer":            "= interface{TableName() string}",
		root + "TypeMapper":            "= func(string, internal/migrate.TypeOptions) string",
		root + "ExecFunc":              "func(context.Context, Executor, string, []any) (sql.Result, error)",
	} {
		if got := sig[key]; got != want {
			t.Errorf("%s: sig=%q, quería %q", key, got, want)
		}
	}
}

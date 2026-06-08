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
	pkgs, err := packages.Load(&packages.Config{Mode: loadMode}, "github.com/jcsvwinston/quark")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("%d errores de carga", n)
	}

	kind := map[string]string{}
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		for _, s := range extract(p) {
			kind[s.Key()] = s.Kind
		}
	}

	const root = "github.com/jcsvwinston/quark."
	mustHave := map[string]string{
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
		root + "cloneForGroup",   // no exportado
		root + "(Nullable).Scan", // alias genérico → sin métodos de sql.Null
	}
	for _, key := range mustNotHave {
		if _, ok := kind[key]; ok {
			t.Errorf("símbolo que NO debía estar en el denominador: %s", key)
		}
	}
}

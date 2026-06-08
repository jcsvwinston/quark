// Command gen-apisurface genera apisurface.json: el DENOMINADOR del gate de
// cobertura del superapp — todo símbolo exportado de la superficie pública de
// Quark (el paquete raíz + los subpaquetes públicos). Usa go/packages + go/types
// (no reflexión), así que se puede `go install`ear y correr en CI.
//
//	go run ./examples/superapp/cmd/gen-apisurface            # escribe examples/superapp/apisurface.json
//	go run ./examples/superapp/cmd/gen-apisurface -out=/tmp/x.json
//
// El formato lo define control.Manifest/Symbol (fuente única); el reconciliador
// de control/manifest.go cruza esto contra lo que el recorder marca como
// invocado, y el gate falla por cada símbolo in-scope no ejercido (salvo
// allowlist.json).
//
//go:generate go run . -out=../../apisurface.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jcsvwinston/quark/examples/superapp/control"
	"golang.org/x/tools/go/packages"
)

// inScope son los paquetes públicos cuyo símbolo exportado cuenta como
// superficie. cmd/quark queda FUERA: es package main y su contrato es la
// interfaz cobra (cubierta aparte por examples/superapp/cli).
var inScope = []string{
	"github.com/jcsvwinston/quark",
	"github.com/jcsvwinston/quark/cache/memory",
	"github.com/jcsvwinston/quark/cache/redis",
	"github.com/jcsvwinston/quark/otel",
	"github.com/jcsvwinston/quark/migrate",
	"github.com/jcsvwinston/quark/quarkmigrate",
	"github.com/jcsvwinston/quark/quarktenant",
}

const loadMode = packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps

func main() {
	out := flag.String("out", filepath.Join("examples", "superapp", "apisurface.json"), "ruta de salida")
	stamp := flag.Bool("stamp", false, "incluir generated_at (off por defecto: fichero determinista para versionar)")
	flag.Parse()

	pkgs, err := packages.Load(&packages.Config{Mode: loadMode}, inScope...)
	if err != nil {
		fail("cargando paquetes: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		fail("hubo errores de carga (ver arriba)")
	}

	var syms []control.Symbol
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			fmt.Fprintf(os.Stderr, "gen-apisurface: aviso: %s cargó sin tipos (se omite)\n", pkg.PkgPath)
			continue
		}
		syms = append(syms, extract(pkg)...)
	}
	sort.Slice(syms, func(i, j int) bool { return syms[i].Key() < syms[j].Key() })

	m := control.Manifest{Symbols: syms}
	if *stamp {
		now := time.Now().UTC()
		m.GeneratedAt = &now
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fail("marshal: %v", err)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fail("escribiendo %q: %v", *out, err)
	}
	fmt.Printf("apisurface.json: %d símbolos sobre %d paquetes → %s\n", len(syms), len(inScope), *out)
}

// extract enumera los símbolos exportados del scope de un paquete: funcs, tipos
// (con sus métodos exportados), vars y consts.
func extract(pkg *packages.Package) []control.Symbol {
	var syms []control.Symbol
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *types.Func:
			syms = append(syms, control.Symbol{Pkg: pkg.PkgPath, Name: o.Name(), Kind: "func"})
		case *types.TypeName:
			syms = append(syms, control.Symbol{Pkg: pkg.PkgPath, Name: o.Name(), Kind: "type"})
			syms = append(syms, methodsOf(pkg.PkgPath, o)...)
		case *types.Var:
			syms = append(syms, control.Symbol{Pkg: pkg.PkgPath, Name: o.Name(), Kind: "var"})
		case *types.Const:
			syms = append(syms, control.Symbol{Pkg: pkg.PkgPath, Name: o.Name(), Kind: "const"})
		}
	}
	return syms
}

// methodsOf devuelve los métodos exportados de un tipo nombrado. Para tipos
// concretos usa los métodos declarados (incluye receptores puntero y valor);
// para interfaces, los métodos del contrato. Render: `(*Query[T]).List` /
// `(Account).TableName` / `(Dialect).Quote`.
//
// Límite conocido: `named.NumMethods()` sólo da los métodos DECLARADOS en el
// tipo, no los promovidos por embedding. Hoy es correcto para el ORM (ningún
// tipo exportado tiene superficie pública relevante sólo vía promoción). Si en
// el futuro un tipo embebe otro con métodos exportados de superficie, revisar
// con `types.NewMethodSet(types.NewPointer(named))`.
func methodsOf(pkgPath string, tn *types.TypeName) []control.Symbol {
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return nil
	}
	recv := typeName(named)
	var syms []control.Symbol

	if iface, ok := named.Underlying().(*types.Interface); ok {
		for i := 0; i < iface.NumMethods(); i++ {
			m := iface.Method(i)
			if m.Exported() {
				syms = append(syms, control.Symbol{Pkg: pkgPath, Name: fmt.Sprintf("(%s).%s", recv, m.Name()), Kind: "method"})
			}
		}
		return syms
	}

	for i := 0; i < named.NumMethods(); i++ {
		m := named.Method(i)
		if !m.Exported() {
			continue
		}
		star := ""
		if sig, ok := m.Type().(*types.Signature); ok && sig.Recv() != nil {
			if _, isPtr := sig.Recv().Type().(*types.Pointer); isPtr {
				star = "*"
			}
		}
		syms = append(syms, control.Symbol{Pkg: pkgPath, Name: fmt.Sprintf("(%s%s).%s", star, recv, m.Name()), Kind: "method"})
	}
	return syms
}

// typeName renderiza el nombre del tipo con sus parámetros genéricos: Query[T].
func typeName(named *types.Named) string {
	s := named.Obj().Name()
	if tp := named.TypeParams(); tp != nil && tp.Len() > 0 {
		parts := make([]string, tp.Len())
		for i := 0; i < tp.Len(); i++ {
			parts[i] = tp.At(i).Obj().Name()
		}
		s += "[" + strings.Join(parts, ", ") + "]"
	}
	return s
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "gen-apisurface: "+format+"\n", a...)
	os.Exit(1)
}

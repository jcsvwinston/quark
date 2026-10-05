// Command gen-apisurface genera apisurface.json: el DENOMINADOR del gate de
// cobertura del superapp — todo símbolo exportado de la superficie pública de
// Quark (el paquete raíz + los subpaquetes públicos). Usa go/packages + go/types
// (no reflexión), así que se puede `go install`ear y correr en CI.
//
//	cd acceptance && go run ./cmd/gen-apisurface        # escribe apisurface.json
//	cd acceptance && go run ./cmd/gen-apisurface -out=/tmp/x.json
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
	"sort"
	"strings"
	"time"

	"github.com/jcsvwinston/quark/acceptance/control"
	"golang.org/x/tools/go/packages"
)

// inScope son los paquetes públicos cuyo símbolo exportado cuenta como
// superficie. cmd/quark queda FUERA: es package main y su contrato es la
// interfaz cobra (cubierta aparte por acceptance/cli).
var inScope = []string{
	"github.com/jcsvwinston/quark",
	"github.com/jcsvwinston/quark/cache/memory",
	"github.com/jcsvwinston/quark/cache/redis",
	"github.com/jcsvwinston/quark/otel",
	"github.com/jcsvwinston/quark/migrate",
	"github.com/jcsvwinston/quark/seed",
	"github.com/jcsvwinston/quark/quarkmigrate",
	"github.com/jcsvwinston/quark/quarktenant",
	"github.com/jcsvwinston/quark/quarktest",
	// El contrato de los módulos de driver (ADR-0023) es superficie pública
	// como cualquier otra: quien escribe un driver de terceros programa
	// contra él. Además EventListener y EventPayload son ALIAS de tipos de
	// aquí, así que sin esta entrada sus métodos desaparecerían del
	// inventario sin que nadie los hubiera quitado.
	"github.com/jcsvwinston/quark/quarkdriver",
	"github.com/jcsvwinston/quark/quarkdriver/drivertest",
	// La suite de motor pública (A11 Q4): un módulo de driver de terceros la
	// importa y la corre contra su motor.
	"github.com/jcsvwinston/quark/quarkdriver/drivertest/suite",
}

const loadMode = packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps

// inScopeSet answers whether a package path is one this file inventories.
var inScopeSet = func() map[string]bool {
	m := make(map[string]bool, len(inScope))
	for _, p := range inScope {
		m[p] = true
	}
	return m
}()

func main() {
	// Relativa al directorio del módulo del superapp (ADR-0024).
	out := flag.String("out", "apisurface.json", "ruta de salida")
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
	q := qualifier(pkg.Types)
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *types.Func:
			syms = append(syms, control.Symbol{Pkg: pkg.PkgPath, Name: o.Name(), Kind: "func", Sig: funcSig(o.Type().(*types.Signature), q)})
		case *types.TypeName:
			syms = append(syms, control.Symbol{Pkg: pkg.PkgPath, Name: o.Name(), Kind: "type", AliasOf: aliasTarget(o), Sig: typeSig(o, q)})
			syms = append(syms, methodsOf(pkg.PkgPath, o, q)...)
		case *types.Var:
			syms = append(syms, control.Symbol{Pkg: pkg.PkgPath, Name: o.Name(), Kind: "var", Sig: types.TypeString(o.Type(), q)})
		case *types.Const:
			syms = append(syms, control.Symbol{Pkg: pkg.PkgPath, Name: o.Name(), Kind: "const", Sig: types.TypeString(o.Type(), q)})
		}
	}
	return syms
}

// --- signatures ---------------------------------------------------------------
//
// The sig field is what makes this file a freeze of the contract and not of
// its names (A11 Q6, CON-02): a parameter of Dialect.UpsertSQL that changes
// type, a predicate added to quarkdriver.Classifier, an interface that grows
// an unexported method — each moves one line of the file, which CI then
// reports as stale with the diff. The rendering is deterministic and written
// to be read in that diff:
//
//   - A type of the symbol's own package is not qualified. A type of another
//     package of this module is qualified by its path inside the module
//     (quarkdriver.LockOptions, internal/migrate.TypeOptions, quark.Client
//     for the root), so the public migrate package and the internal one do
//     not read the same; any other by its package name (context.Context,
//     sql.Rows).
//   - A signature carries the types of its parameters and results and drops
//     their names, so renaming a parameter is not a change; a variadic last
//     parameter is written ...T.
//   - A struct lists its exported fields in declaration order; an interface
//     whose methods are symbols of their own is written "interface", or
//     "interface (sealed)" when it has an unexported method, because a third
//     party can no longer implement it.
//
// internal/extbench renders the same way from the compiler's export data to
// check what this file records (probeSurfaceFreeze); a change to the rules
// here is a change there.

// modulePath is the library module; its packages are qualified by their
// path inside it.
const modulePath = "github.com/jcsvwinston/quark"

// qualifier writes nothing for the package being inventoried, a package of
// this module by its path inside the module, and any other by its name.
func qualifier(self *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		switch path := p.Path(); {
		case path == self.Path():
			return ""
		case path == modulePath:
			return "quark"
		case strings.HasPrefix(path, modulePath+"/"):
			return strings.TrimPrefix(path, modulePath+"/")
		default:
			return p.Name()
		}
	}
}

// funcSig renders a function or method signature without its receiver:
// func[T any](context.Context, ...any) (*Query[T], error).
func funcSig(sig *types.Signature, q types.Qualifier) string {
	return "func" + typeParamList(sig.TypeParams(), q) + sigBody(sig, q)
}

// sigBody is a signature's parameter and result lists, as an interface
// literal writes them after the method name.
func sigBody(sig *types.Signature, q types.Qualifier) string {
	s := "(" + strings.Join(tupleTypes(sig.Params(), sig.Variadic(), q), ", ") + ")"
	switch res := tupleTypes(sig.Results(), false, q); len(res) {
	case 0:
	case 1:
		s += " " + res[0]
	default:
		s += " (" + strings.Join(res, ", ") + ")"
	}
	return s
}

func tupleTypes(t *types.Tuple, variadic bool, q types.Qualifier) []string {
	out := make([]string, 0, t.Len())
	for i := 0; i < t.Len(); i++ {
		typ := t.At(i).Type()
		if variadic && i == t.Len()-1 {
			if sl, ok := typ.(*types.Slice); ok {
				out = append(out, "..."+types.TypeString(sl.Elem(), q))
				continue
			}
		}
		out = append(out, types.TypeString(typ, q))
	}
	return out
}

func typeParamList(tps *types.TypeParamList, q types.Qualifier) string {
	if tps == nil || tps.Len() == 0 {
		return ""
	}
	parts := make([]string, tps.Len())
	for i := 0; i < tps.Len(); i++ {
		tp := tps.At(i)
		parts[i] = tp.Obj().Name() + " " + types.TypeString(tp.Constraint(), q)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// typeSig renders the definition of a type: its type parameters and its
// underlying type. An alias of a type an in-scope package declares renders
// nothing — alias_of names it, and its methods are listed there. An alias of
// a type of an internal package renders that type's whole definition,
// methods included, because no symbol of this file lists them: quark.TypeMapper
// and quark.TableNamer are the shape of internal types, and a third party
// implements them by that shape.
func typeSig(tn *types.TypeName, q types.Qualifier) string {
	if tn.IsAlias() {
		tparams := ""
		if a, ok := tn.Type().(*types.Alias); ok && a.TypeParams().Len() > 0 {
			tparams = typeParamList(a.TypeParams(), q) + " "
		}
		target := types.Unalias(tn.Type())
		if named, ok := target.(*types.Named); ok && named.Obj().Pkg() != nil {
			path := named.Obj().Pkg().Path()
			if inScopeSet[path] {
				return ""
			}
			if isOwnInternal(path) {
				return tparams + "= " + definition(named, q)
			}
		}
		return tparams + "= " + types.TypeString(target, q)
	}
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return shape(tn.Type().Underlying(), q, false)
	}
	if named.TypeParams().Len() > 0 {
		return typeParamList(named.TypeParams(), q) + " " + shape(named.Underlying(), q, false)
	}
	return shape(named.Underlying(), q, false)
}

// isOwnInternal reports whether path is an internal package of this module,
// whose types reach the public API only through an alias.
func isOwnInternal(path string) bool {
	return strings.HasPrefix(path, modulePath+"/") &&
		(strings.Contains(path, "/internal/") || strings.HasSuffix(path, "/internal"))
}

// shape renders an underlying type. full spells an interface's methods out;
// otherwise they are symbols of their own and the interface is "interface".
func shape(u types.Type, q types.Qualifier, full bool) string {
	switch u := u.(type) {
	case *types.Struct:
		var fields []string
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if !f.Exported() {
				continue
			}
			if f.Embedded() {
				fields = append(fields, types.TypeString(f.Type(), q))
			} else {
				fields = append(fields, f.Name()+" "+types.TypeString(f.Type(), q))
			}
		}
		return "struct{" + strings.Join(fields, "; ") + "}"
	case *types.Interface:
		if !u.IsMethodSet() {
			return types.TypeString(u, q) // a constraint: its type set is the contract
		}
		sealed := false
		var methods []string
		for i := 0; i < u.NumMethods(); i++ {
			m := u.Method(i)
			if !m.Exported() {
				sealed = true
				continue
			}
			methods = append(methods, m.Name()+sigBody(m.Type().(*types.Signature), q))
		}
		switch {
		case full && sealed:
			return "interface{" + strings.Join(methods, "; ") + "} (sealed)"
		case full:
			return "interface{" + strings.Join(methods, "; ") + "}"
		case sealed:
			return "interface (sealed)"
		default:
			return "interface"
		}
	case *types.Signature:
		return funcSig(u, q)
	default:
		return types.TypeString(u, q)
	}
}

// definition renders a named type that no symbol of this file lists: its
// underlying type with an interface's methods spelled out, and, for any
// other type, the exported methods of *T.
func definition(named *types.Named, q types.Qualifier) string {
	s := shape(named.Underlying(), q, true)
	if _, isIface := named.Underlying().(*types.Interface); isIface {
		return s
	}
	mset := types.NewMethodSet(types.NewPointer(named))
	var methods []string
	for i := 0; i < mset.Len(); i++ {
		m := mset.At(i).Obj()
		if m.Exported() {
			methods = append(methods, m.Name()+sigBody(m.Type().(*types.Signature), q))
		}
	}
	if len(methods) == 0 {
		return s
	}
	sort.Strings(methods)
	return s + " methods{" + strings.Join(methods, "; ") + "}"
}

// aliasTarget devuelve, para un alias de un tipo NOMBRADO, la clave de ese
// tipo (`github.com/jcsvwinston/quark/quarkdriver.Dialect`); "" para un tipo
// que no es alias o un alias de un tipo sin nombre. methodsOf no lista los
// métodos de un alias —se listan bajo el paquete que declara el tipo—, así que
// esta clave es lo que une en apisurface.json el nombre que se queda con los
// métodos que se fueron: quark.Dialect apunta a quarkdriver.Dialect, donde
// están los 21 (Dialect).X desde ADR-0026.
func aliasTarget(tn *types.TypeName) string {
	if !tn.IsAlias() {
		return ""
	}
	named, ok := types.Unalias(tn.Type()).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name()
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
func methodsOf(pkgPath string, tn *types.TypeName, q types.Qualifier) []control.Symbol {
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
				syms = append(syms, control.Symbol{Pkg: pkgPath, Name: fmt.Sprintf("(%s).%s", recv, m.Name()), Kind: "method", Sig: funcSig(m.Type().(*types.Signature), q)})
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
		syms = append(syms, control.Symbol{Pkg: pkgPath, Name: fmt.Sprintf("(%s%s).%s", star, recv, m.Name()), Kind: "method", Sig: funcSig(m.Type().(*types.Signature), q)})
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

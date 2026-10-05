package control

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Symbol es un identificador exportado de la superficie pública de Quark.
type Symbol struct {
	Pkg  string `json:"pkg"`  // p.ej. github.com/jcsvwinston/quark
	Name string `json:"name"` // p.ej. (*Query[T]).UpsertBatch o WithReplicas
	Kind string `json:"kind"` // func | method | type | var
	// AliasOf, en un tipo que es ALIAS (`type Dialect = quarkdriver.Dialect`),
	// es la clave (pkg.Nombre) del tipo nombrado al que apunta. Los métodos de
	// un alias no se listan bajo él sino bajo el tipo que los declara, así que
	// sin este campo un tipo que se MUEVE dejando un alias (ADR-0026) se leía
	// en el diff como métodos quitados de un paquete y añadidos a otro, sin
	// nada que uniera las dos mitades. No es una firma: no fija parámetros.
	AliasOf string `json:"alias_of,omitempty"`
	// Sig is the shape the symbol's contract fixes: the signature of a func
	// or a method (parameter and result types, without names), the type of a
	// var or a const, and the definition of a type — a struct's exported
	// fields, a func type's signature, "interface" for an interface whose
	// methods are listed as symbols of their own. An alias of a type declared
	// in an in-scope package leaves it empty (alias_of says where, and the
	// methods are listed under the declaring type); an alias of an internal
	// type writes the whole definition of the type it names, because nothing
	// else lists it. Without this field the file froze NAMES: changing a
	// parameter of Dialect.UpsertSQL left it byte-identical (A11 Q6, control
	// CON-02 of internal/extbench).
	Sig string `json:"sig,omitempty"`
}

// Key es la clave canónica usada en cobertura y allowlist.
func (s Symbol) Key() string { return s.Pkg + "." + s.Name }

// Manifest es el denominador: TODO lo que Quark expone. Lo genera
// cmd/gen-apisurface con go/packages — nunca se edita a mano.
//
// GeneratedAt es un puntero con omitempty: el fichero versionado se genera SIN
// timestamp (determinista, para que un símbolo público nuevo produzca un diff
// limpio y CI pueda exigir regenerar). Sólo se rellena en corridas ad-hoc.
type Manifest struct {
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	Symbols     []Symbol   `json:"symbols"`
}

// LoadManifest lee apisurface.json.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest parse: %w", err)
	}
	return &m, nil
}

// Allowlist son los símbolos conscientemente fuera de scope, con justificación.
type Allowlist struct {
	Reasons map[string]string `json:"reasons"` // Symbol.Key() -> motivo
}

// LoadAllowlist lee allowlist.json. Si no existe, devuelve una allowlist vacía
// (sin justificaciones) junto al error, para que el caller decida.
func LoadAllowlist(path string) (Allowlist, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Allowlist{Reasons: map[string]string{}}, fmt.Errorf("allowlist: %w", err)
	}
	var a Allowlist
	if err := json.Unmarshal(b, &a); err != nil {
		return Allowlist{Reasons: map[string]string{}}, fmt.Errorf("allowlist parse: %w", err)
	}
	if a.Reasons == nil {
		a.Reasons = map[string]string{}
	}
	return a, nil
}

// Has indica si key (Symbol.Key) está justificado fuera de scope.
func (a Allowlist) Has(key string) bool {
	_, ok := a.Reasons[key]
	return ok
}

// Invoked registra qué símbolos se ejercieron en cada motor. Lo alimenta el
// recorder en runtime (paquete recorder/).
type Invoked map[Engine]map[string]bool

// Reconcile compara el manifiesto contra lo invocado por motor y emite una celda
// MISSING por cada símbolo in-scope no ejercido en cada motor. No emite
// PASS/FAIL: eso lo deciden las aserciones funcionales de los exercisers.
func (m *Manifest) Reconcile(inv Invoked, allow Allowlist) []Cell {
	var cells []Cell
	for _, e := range AllEngines() {
		seen := inv[e]
		for _, s := range m.Symbols {
			key := s.Key()
			if allow.Has(key) {
				continue
			}
			if seen == nil || !seen[key] {
				cells = append(cells, Cell{Method: key, Engine: e, Status: StatusMissing, Detail: "no invocado"})
			}
		}
	}
	return cells
}

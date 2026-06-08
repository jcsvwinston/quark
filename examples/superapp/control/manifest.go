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

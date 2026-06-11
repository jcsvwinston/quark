package exercise

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/engine"
)

// El oráculo de paridad cierra el hueco que los asserts por-motor no pueden
// ver: una divergencia SILENCIOSA entre motores (mismo input, resultados
// distintos, ningún error). Cada sonda corre la MISMA operación sobre el mismo
// dataset determinista en cada motor y reduce el resultado a una forma
// canónica comparable; al final, todos los motores deben haber producido
// byte-a-byte el mismo payload por sonda.
//
// Reglas de canonicalización (la parte honesta del oráculo — diferencias que
// NO son bugs sino semántica documentada del motor):
//
//   - Oracle persiste ” como NULL: todo string nullable se canoniza con
//     parityCanonString (vacío-o-null → "∅").
//   - Precisión temporal: cada motor guarda fracciones distintas (MySQL
//     DATETIME(0), PG microsegundos…) — todo time.Time va a UTC truncado al
//     segundo, RFC3339.
//   - Floats (AVG): el tipo de retorno difiere (NUMERIC vs float) — se
//     canoniza a 6 decimales.
//   - Los IDs autoincrement NUNCA entran en el payload: el dataset se
//     identifica por claves naturales (ref) y se ordena por ellas.
type parityItem struct {
	ID    int64                  `db:"id" pk:"true"`
	Ref   string                 `db:"ref" quark:"unique,not_null"`
	Grp   string                 `db:"grp" quark:"not_null"`
	Score int64                  `db:"score" default:"0"`
	Note  quark.Nullable[string] `db:"note"`
	Flag  bool                   `db:"flag"`
	At    time.Time              `db:"at"`
}

func (parityItem) TableName() string { return "parity_items" }

// parityChild prueba la forma del preload (conteo de hijos por padre, no IDs).
type parityChild struct {
	ID     int64  `db:"id" pk:"true"`
	ItemID int64  `db:"item_id" quark:"not_null"`
	Tag    string `db:"tag" quark:"not_null"`
}

func (parityChild) TableName() string { return "parity_children" }

// parityBaseTime es fijo: el dataset debe ser idéntico entre motores y entre
// runs (nada de time.Now en datos comparados).
var parityBaseTime = time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)

func parityCanonString(n quark.Nullable[string]) string {
	if !n.Valid || n.V == "" {
		return "∅" // Oracle: '' ≡ NULL — ambos canonizan igual
	}
	return n.V
}

func parityCanonTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func parityCanonFloat(f float64) string { return fmt.Sprintf("%.6f", f) }

// ParityPayload es el resultado canónico de un motor: sonda → JSON canónico
// (json.Marshal ordena las keys de los maps — encoding estable).
type ParityPayload map[string]string

// parityProbe define una sonda: corre contra un client y devuelve un valor
// JSON-serializable YA canonicalizado.
type parityProbe struct {
	Name string
	Run  func(ctx context.Context, c *quark.Client) (any, error)
}

// parityProbes son las sondas del oráculo. Sólo lecturas sobre el dataset
// sembrado por seedParity; deterministas (orden por clave natural siempre).
var parityProbes = []parityProbe{
	{"rows_ordered", func(ctx context.Context, c *quark.Client) (any, error) {
		rows, err := quark.For[parityItem](ctx, c).OrderBy("ref", "ASC").Limit(100).List()
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{
				"ref": r.Ref, "grp": r.Grp, "score": r.Score,
				"note": parityCanonString(r.Note), "flag": r.Flag,
				"at": parityCanonTime(r.At),
			})
		}
		return out, nil
	}},
	{"count_filtered", func(ctx context.Context, c *quark.Client) (any, error) {
		return quark.For[parityItem](ctx, c).Where("score", ">=", 20).Count()
	}},
	{"aggregates", func(ctx context.Context, c *quark.Client) (any, error) {
		sum, err := quark.For[parityItem](ctx, c).Sum("score")
		if err != nil {
			return nil, err
		}
		avg, err := quark.For[parityItem](ctx, c).Avg("score")
		if err != nil {
			return nil, err
		}
		min, err := quark.For[parityItem](ctx, c).Min("score")
		if err != nil {
			return nil, err
		}
		max, err := quark.For[parityItem](ctx, c).Max("score")
		if err != nil {
			return nil, err
		}
		return map[string]string{
			"sum": parityCanonFloat(sum), "avg": parityCanonFloat(avg),
			"min": parityCanonFloat(min), "max": parityCanonFloat(max),
		}, nil
	}},
	{"group_by", func(ctx context.Context, c *quark.Client) (any, error) {
		// Conteo por grupo, reconstruido en Go para no depender del shape del
		// GROUP BY scan: orden por grp.
		rows, err := quark.For[parityItem](ctx, c).OrderBy("ref", "ASC").Limit(100).List()
		if err != nil {
			return nil, err
		}
		counts := map[string]int{}
		for _, r := range rows {
			counts[r.Grp]++
		}
		return counts, nil
	}},
	{"distinct_grps", func(ctx context.Context, c *quark.Client) (any, error) {
		rows, err := quark.For[parityItem](ctx, c).Select("grp").Distinct().OrderBy("grp", "ASC").Limit(100).List()
		if err != nil {
			return nil, err
		}
		grps := make([]string, 0, len(rows))
		for _, r := range rows {
			grps = append(grps, r.Grp)
		}
		// Doble ordenación a propósito: el OrderBy ya ordena en el motor, pero
		// el sort en Go blinda la sonda contra collations divergentes (ASCII
		// puro aquí, aún así la defensa es gratis).
		sort.Strings(grps)
		return grps, nil
	}},
	{"pagination_page2", func(ctx context.Context, c *quark.Client) (any, error) {
		rows, err := quark.For[parityItem](ctx, c).OrderBy("ref", "ASC").Offset(2).Limit(2).List()
		if err != nil {
			return nil, err
		}
		refs := make([]string, 0, len(rows))
		for _, r := range rows {
			refs = append(refs, r.Ref)
		}
		return refs, nil
	}},
	{"preload_shape", func(ctx context.Context, c *quark.Client) (any, error) {
		// Conteo de hijos por ref del padre — forma del preload sin IDs.
		items, err := quark.For[parityItem](ctx, c).OrderBy("ref", "ASC").Limit(100).List()
		if err != nil {
			return nil, err
		}
		kids, err := quark.For[parityChild](ctx, c).OrderBy("tag", "ASC").Limit(100).List()
		if err != nil {
			return nil, err
		}
		byItem := map[int64]string{}
		for _, it := range items {
			byItem[it.ID] = it.Ref
		}
		shape := map[string][]string{}
		for _, k := range kids {
			ref := byItem[k.ItemID]
			shape[ref] = append(shape[ref], k.Tag)
		}
		for _, tags := range shape {
			sort.Strings(tags)
		}
		return shape, nil
	}},
	{"empty_string_vs_null", func(ctx context.Context, c *quark.Client) (any, error) {
		// EL caso Oracle: la fila sembrada con Note="" debe canonizar igual que
		// la sembrada con Note=NULL en TODOS los motores.
		var out []string
		for _, ref := range []string{"p-empty", "p-null"} {
			row, err := quark.For[parityItem](ctx, c).Where("ref", "=", ref).First()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", ref, err)
			}
			out = append(out, ref+"="+parityCanonString(row.Note))
		}
		return out, nil
	}},
	{"tx_commit_visibility", func(ctx context.Context, c *quark.Client) (any, error) {
		// Un write commiteado en tx es visible después — y el payload final
		// vuelve al estado original (la sonda limpia lo suyo).
		if err := c.Tx(ctx, func(tx *quark.Tx) error {
			return quark.ForTx[parityItem](ctx, tx).Create(&parityItem{
				Ref: "p-tx", Grp: "tx", Score: 99, Flag: true, At: parityBaseTime,
			})
		}); err != nil {
			return nil, err
		}
		n, err := quark.For[parityItem](ctx, c).Where("ref", "=", "p-tx").Count()
		if err != nil {
			return nil, err
		}
		if _, err := quark.For[parityItem](ctx, c).Where("ref", "=", "p-tx").DeleteBy(); err != nil {
			return nil, err
		}
		return n, nil
	}},
}

// seedParity deja las tablas de paridad en el estado canónico (re-ejecutable:
// dropea y recrea — el dataset DEBE ser idéntico entre motores y runs).
func seedParity(ctx context.Context, c *quark.Client) error {
	_, _ = c.Raw().ExecContext(ctx, "DROP TABLE parity_children")
	_, _ = c.Raw().ExecContext(ctx, "DROP TABLE parity_items")
	if err := c.Migrate(ctx, &parityItem{}, &parityChild{}); err != nil {
		return fmt.Errorf("migrate parity: %w", err)
	}
	items := []*parityItem{
		{Ref: "p-a", Grp: "g1", Score: 10, Note: quark.Nullable[string]{V: "alpha", Valid: true}, Flag: true, At: parityBaseTime},
		{Ref: "p-b", Grp: "g1", Score: 20, Note: quark.Nullable[string]{V: "beta", Valid: true}, Flag: false, At: parityBaseTime.Add(time.Hour)},
		{Ref: "p-c", Grp: "g2", Score: 30, Flag: true, At: parityBaseTime.Add(2 * time.Hour)},
		{Ref: "p-empty", Grp: "g2", Score: 40, Note: quark.Nullable[string]{V: "", Valid: true}, Flag: false, At: parityBaseTime},
		{Ref: "p-null", Grp: "g3", Score: 50, Flag: true, At: parityBaseTime},
	}
	if err := quark.For[parityItem](ctx, c).CreateBatch(items); err != nil {
		return fmt.Errorf("seed items: %w", err)
	}
	// Hijos: 2 para p-a, 1 para p-b, 0 para el resto.
	byRef := map[string]int64{}
	for _, it := range items {
		byRef[it.Ref] = it.ID
	}
	kids := []*parityChild{
		{ItemID: byRef["p-a"], Tag: "k1"},
		{ItemID: byRef["p-a"], Tag: "k2"},
		{ItemID: byRef["p-b"], Tag: "k3"},
	}
	if err := quark.For[parityChild](ctx, c).CreateBatch(kids); err != nil {
		return fmt.Errorf("seed children: %w", err)
	}
	return nil
}

// RunParity corre el oráculo: por motor (vía engine.Run, con su leak-check),
// siembra el dataset canónico, ejecuta cada sonda y reduce su resultado a JSON
// canónico. Devuelve el payload por motor y el primer error por motor.
func RunParity(conns map[control.Engine]engine.Conn, tol int) (map[control.Engine]ParityPayload, map[control.Engine]error) {
	payloads := map[control.Engine]ParityPayload{}
	newClient := func(c engine.Conn) (*quark.Client, error) {
		l := quark.DefaultLimits()
		l.SafeMigrations = false
		return quark.New(c.Driver, c.DSN, quark.WithLimits(l))
	}
	fn := func(e control.Engine, client *quark.Client) error {
		ctx := context.Background()
		if err := seedParity(ctx, client); err != nil {
			return err
		}
		defer func() {
			_, _ = client.Raw().ExecContext(context.Background(), "DROP TABLE parity_children")
			_, _ = client.Raw().ExecContext(context.Background(), "DROP TABLE parity_items")
		}()
		p := ParityPayload{}
		for _, probe := range parityProbes {
			v, err := probe.Run(ctx, client)
			if err != nil {
				return fmt.Errorf("sonda %s: %w", probe.Name, err)
			}
			b, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("sonda %s: marshal: %w", probe.Name, err)
			}
			p[probe.Name] = string(b)
		}
		payloads[e] = p
		return nil
	}
	res := engine.Run(conns, tol, newClient, fn)
	errs := map[control.Engine]error{}
	for e, r := range res {
		if r.Err != nil {
			errs[e] = r.Err
		}
	}
	return payloads, errs
}

// ParityDivergence describe una sonda cuyo payload difiere entre motores.
type ParityDivergence struct {
	Probe  string
	Values map[control.Engine]string
}

func (d ParityDivergence) String() string {
	parts := make([]string, 0, len(d.Values))
	for e, v := range d.Values {
		parts = append(parts, fmt.Sprintf("%s=%s", e, v))
	}
	sort.Strings(parts)
	return fmt.Sprintf("sonda %q diverge: %s", d.Probe, strings.Join(parts, " | "))
}

// CompareParity contrasta los payloads de todos los motores sonda a sonda.
// Una sonda ausente en algún motor también es divergencia (no hay sondas
// opcionales: el dataset y las operaciones son portables a los 6).
func CompareParity(payloads map[control.Engine]ParityPayload) []ParityDivergence {
	if len(payloads) < 2 {
		return nil // con un solo motor no hay con quién divergir
	}
	var divs []ParityDivergence
	for _, probe := range parityProbes {
		values := map[control.Engine]string{}
		distinct := map[string]bool{}
		for e, p := range payloads {
			v, ok := p[probe.Name]
			if !ok {
				v = "<ausente>"
			}
			values[e] = v
			distinct[v] = true
		}
		if len(distinct) > 1 {
			divs = append(divs, ParityDivergence{Probe: probe.Name, Values: values})
		}
	}
	return divs
}

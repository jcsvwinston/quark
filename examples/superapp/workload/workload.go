// Package workload ejerce el dominio de la superapp a ALTO VOLUMEN —datos
// relacionados, consultas, transacciones y caché— y recolecta métricas para un
// informe ejecutivo. El recorder (S2) es la fuente de latencias y perfil SQL:
// captura cada statement con su duración, op y filas. La caché (in-memory o
// Redis) y el logger los instala el caller; aquí solo se ejerce la superficie.
package workload

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// Config parametriza la corrida.
type Config struct {
	Engine string // etiqueta del motor (sqlite/postgres/…)
	Scale  int    // multiplicador de volumen (>=1)
}

// LatencyStat son percentiles de latencia (ms) de un grupo de statements.
type LatencyStat struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

// Result reúne todas las métricas de una corrida (se serializa a metrics.json y
// alimenta el informe ejecutivo).
type Result struct {
	Engine        string        `json:"engine"`
	Scale         int           `json:"scale"`
	StartedAt     time.Time     `json:"started_at"`
	SeedDuration  time.Duration `json:"seed_duration_ns"`
	WorkDuration  time.Duration `json:"work_duration_ns"`
	TotalDuration time.Duration `json:"total_duration_ns"`

	Volume    map[string]int64 `json:"volume"`
	TotalRows int64            `json:"total_rows"`

	OpsByCategory map[string]int `json:"ops_by_category"`
	Transactions  int            `json:"transactions"`
	Errors        int            `json:"errors"`

	StmtsByOp    map[string]int `json:"stmts_by_op"`
	TotalStmts   int            `json:"total_stmts"`
	RowsSelected int64          `json:"rows_selected"` // filas devueltas por SELECT (lecturas reales)
	RowsReturned int64          `json:"rows_returned"` // filas devueltas por INSERT/UPDATE/DELETE … RETURNING

	Latency map[string]LatencyStat `json:"latency"` // exec/query/query_row/overall

	CacheableReads int `json:"cacheable_reads"`
	CacheHits      int `json:"cache_hits"`
}

// CacheHitRate devuelve el ratio de aciertos de caché [0,1].
func (r *Result) CacheHitRate() float64 {
	if r.CacheableReads == 0 {
		return 0
	}
	return float64(r.CacheHits) / float64(r.CacheableReads)
}

// RowsPerSec y QueriesPerSec son throughput sobre el wall-clock total.
func (r *Result) RowsPerSec() float64    { return perSec(r.TotalRows, r.TotalDuration) }
func (r *Result) QueriesPerSec() float64 { return perSec(int64(r.TotalStmts), r.TotalDuration) }

func perSec(n int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / d.Seconds()
}

// Run ejecuta el seed de alto volumen + la mezcla de carga y devuelve las
// métricas. client debe traer instalado el recorder rec (vía rec.Options()).
func Run(ctx context.Context, cfg Config, client *quark.Client, rec *recorder.Recorder, log *slog.Logger) (*Result, error) {
	if cfg.Scale < 1 {
		cfg.Scale = 1
	}
	res := &Result{
		Engine: cfg.Engine, Scale: cfg.Scale, StartedAt: time.Now(),
		Volume: map[string]int64{}, OpsByCategory: map[string]int{},
		StmtsByOp: map[string]int{}, Latency: map[string]LatencyStat{},
	}
	track := func(cat string, err error) {
		res.OpsByCategory[cat]++
		if err != nil {
			res.Errors++
			log.Warn("operación falló", "category", cat, "err", err)
		}
	}
	// seedBatch aborta la corrida si una inserción de seed falla: un seed parcial
	// produciría FKs colgando y un volumen corrupto que parecería un resultado
	// válido — peor que no tener informe.
	seedBatch := func(label string, err error) error {
		res.OpsByCategory["batch_insert"]++
		if err != nil {
			return fmt.Errorf("seed %s: %w", label, err)
		}
		return nil
	}

	var emailSeq int
	nextEmail := func() string { emailSeq++; return fmt.Sprintf("user%07d@superapp.test", emailSeq) }

	// ============================ SEED (alto volumen) ============================
	seedStart := time.Now()
	nAccounts := 1000 * cfg.Scale
	projectsPer, tasksPer := 4, 5
	log.Info("seed: comienzo", "engine", cfg.Engine, "scale", cfg.Scale, "accounts_objetivo", nAccounts)

	// Accounts.
	accounts := make([]*domain.Account, nAccounts)
	for i := range accounts {
		accounts[i] = &domain.Account{
			Email: nextEmail(), Name: fmt.Sprintf("Account %d", i), Role: "member", Active: true,
			Settings: quark.JSON[domain.AccountPrefs]{V: domain.AccountPrefs{Theme: "dark", Locale: "es"}},
			Tags:     quark.Array[string]{V: []string{"seed"}},
		}
	}
	if err := seedBatch("accounts", quark.For[domain.Account](ctx, client).CreateBatch(accounts)); err != nil {
		return nil, err
	}

	// Tags.
	tags := make([]*domain.Tag, 20)
	for i := range tags {
		tags[i] = &domain.Tag{Slug: fmt.Sprintf("tag-%02d", i)}
	}
	if err := seedBatch("tags", quark.For[domain.Tag](ctx, client).CreateBatch(tags)); err != nil {
		return nil, err
	}

	// Projects (belongs_to account).
	projects := make([]*domain.Project, 0, nAccounts*projectsPer)
	for _, a := range accounts {
		for j := 0; j < projectsPer; j++ {
			projects = append(projects, &domain.Project{
				OwnerID: a.ID, Name: fmt.Sprintf("Project %d-%d", a.ID, j), Status: "draft",
			})
		}
	}
	if err := seedBatch("projects", quark.For[domain.Project](ctx, client).CreateBatch(projects)); err != nil {
		return nil, err
	}

	// Tasks (belongs_to project + assignee nullable FK — el caso BB-5).
	tasks := make([]*domain.Task, 0, len(projects)*tasksPer)
	for idx, p := range projects {
		for j := 0; j < tasksPer; j++ {
			t := &domain.Task{
				ProjectID: p.ID, Title: fmt.Sprintf("Task %d-%d", p.ID, j),
				Priority: (idx + j) % 5,
			}
			if j%2 == 0 && len(accounts) > 0 { // mitad con assignee
				id := accounts[idx%len(accounts)].ID
				t.AssigneeID = &id
			}
			tasks = append(tasks, t)
		}
	}
	if err := seedBatch("tasks", quark.For[domain.Task](ctx, client).CreateBatch(tasks)); err != nil {
		return nil, err
	}

	// Memberships (PK compuesta).
	memberships := make([]*domain.Membership, 0, nAccounts*2)
	for ai, a := range accounts {
		for j := 0; j < 2 && ai*projectsPer+j < len(projects); j++ {
			memberships = append(memberships, &domain.Membership{
				AccountID: a.ID, ProjectID: projects[ai*projectsPer+j].ID,
				Role: "member", JoinedAt: time.Now().UTC(),
			})
		}
	}
	if err := seedBatch("memberships", quark.For[domain.Membership](ctx, client).CreateBatch(memberships)); err != nil {
		return nil, err
	}

	// Attachments ([]byte + Nullable[[]byte] — el caso BB-6).
	attachments := make([]*domain.Attachment, 0, len(tasks)/5+1)
	for i, t := range tasks {
		if i%5 != 0 {
			continue
		}
		attachments = append(attachments, &domain.Attachment{
			TaskID: t.ID, Name: fmt.Sprintf("att-%d.bin", t.ID), Bytes: []byte("payload"),
		})
	}
	if err := seedBatch("attachments", quark.For[domain.Attachment](ctx, client).CreateBatch(attachments)); err != nil {
		return nil, err
	}

	res.SeedDuration = time.Since(seedStart)
	log.Info("seed: fin", "duracion", res.SeedDuration.String())

	// ============================ CARGA (queries / tx / cache) ============================
	workStart := time.Now()

	// 1. Lecturas variadas (where / paginate / first / count) para perfil de latencia.
	readN := 200 * cfg.Scale
	for i := 0; i < readN; i++ {
		switch i % 4 {
		case 0:
			_, err := quark.For[domain.Task](ctx, client).Where("priority", ">", 2).OrderBy("id", "DESC").Limit(50).List()
			track("read", err)
		case 1:
			_, err := quark.For[domain.Account](ctx, client).Paginate(50, 1+(i%10))
			track("read", err)
		case 2:
			_, err := quark.For[domain.Account](ctx, client).Where("email", "=", fmt.Sprintf("user%07d@superapp.test", 1+(i%nAccounts))).First()
			track("read", err)
		case 3:
			_, err := quark.For[domain.Task](ctx, client).Where("done", "=", false).Count()
			track("read", err)
		}
	}

	// 2. Preload de relaciones (has_many / belongs_to). Guardado: si el motor o la
	//    relación no lo soporta, cuenta como error y sigue.
	if _, err := quark.For[domain.Account](ctx, client).Preload("Projects").Limit(20).List(); err != nil {
		track("preload", err)
	} else {
		track("preload", nil)
	}
	if _, err := quark.For[domain.Task](ctx, client).Preload("Project").Preload("Assignee").Limit(50).List(); err != nil {
		track("preload", err)
	} else {
		track("preload", nil)
	}

	// 3. Transacciones atómicas multi-entidad.
	txN := 50 * cfg.Scale
	for i := 0; i < txN; i++ {
		err := client.Tx(ctx, func(tx *quark.Tx) error {
			a := &domain.Account{Email: nextEmail(), Name: "tx-user", Role: "member", Active: true}
			if err := quark.ForTx[domain.Account](ctx, tx).Create(a); err != nil {
				return err
			}
			for j := 0; j < 2; j++ {
				p := &domain.Project{OwnerID: a.ID, Name: fmt.Sprintf("tx-proj-%d-%d", a.ID, j), Status: "active"}
				if err := quark.ForTx[domain.Project](ctx, tx).Create(p); err != nil {
					return err
				}
			}
			return nil
		})
		track("transaction", err)
		if err == nil {
			res.Transactions++
		}
	}

	// 4. Updates (single-row).
	updTasks, err := quark.For[domain.Task](ctx, client).Where("done", "=", false).Limit(200 * cfg.Scale).List()
	if err != nil {
		track("read", err)
	}
	for i := range updTasks {
		t := updTasks[i]
		t.Done = true
		_, uerr := quark.For[domain.Task](ctx, client).Update(&t)
		track("update", uerr)
	}

	// 5. Deletes (soft-delete de projects 'draft' — tienen deleted_at).
	delProjects, err := quark.For[domain.Project](ctx, client).Where("status", "=", "draft").Limit(100 * cfg.Scale).List()
	if err != nil {
		track("read", err)
	}
	for i := range delProjects {
		p := delProjects[i]
		_, derr := quark.For[domain.Project](ctx, client).Delete(&p)
		track("delete", derr)
	}

	// 6. Lecturas cacheadas: misma query repetida → 1 miss + N hits (hit-rate).
	cacheN := 200 * cfg.Scale
	for i := 0; i < cacheN; i++ {
		before := rec.Count()
		_, cerr := quark.For[domain.Account](ctx, client).Where("role", "=", "member").Cache(time.Minute).Limit(50).List()
		track("cached_read", cerr)
		if cerr == nil {
			res.CacheableReads++
			if rec.Count() == before {
				res.CacheHits++
			}
		}
	}

	res.WorkDuration = time.Since(workStart)
	res.TotalDuration = time.Since(seedStart)
	log.Info("carga: fin", "duracion", res.WorkDuration.String(), "errores", res.Errors)

	// ============================ MÉTRICAS ============================
	vols, countFails := countAll(ctx, client, log)
	res.Errors += countFails // un Count fallido deja una entidad sin contar → no mentir con "0 errores"
	for name, count := range vols {
		res.Volume[name] = count
		res.TotalRows += count
	}

	stmts := rec.Statements()
	res.TotalStmts = len(stmts)
	durBy := map[string][]time.Duration{}
	var all []time.Duration
	for _, s := range stmts {
		op := string(s.Op)
		res.StmtsByOp[op]++
		durBy[op] = append(durBy[op], s.Dur)
		all = append(all, s.Dur)
		// El conteo de filas solo llega por query/query_row. Separamos SELECT
		// (lecturas reales) de INSERT/UPDATE/DELETE … RETURNING: en SQLite/PG/
		// MariaDB Create/CreateBatch usan RETURNING y transitan por esta misma
		// vía, así que contarlas como "leídas" sería engañoso.
		if s.Op != recorder.OpExec && s.Rows > 0 {
			if sqlVerb(s.SQL) == "SELECT" {
				res.RowsSelected += s.Rows
			} else {
				res.RowsReturned += s.Rows
			}
		}
	}
	for op, ds := range durBy {
		res.Latency[op] = statOf(ds)
	}
	res.Latency["overall"] = statOf(all)

	return res, nil
}

// countAll cuenta las filas activas por entidad. Devuelve además cuántos counts
// fallaron, para que el caller no reporte "0 errores" con el volumen incompleto.
func countAll(ctx context.Context, client *quark.Client, log *slog.Logger) (map[string]int64, int) {
	out := map[string]int64{}
	fails := 0
	count := func(name string, n int64, err error) {
		if err != nil {
			log.Warn("count falló", "entity", name, "err", err)
			fails++
			return
		}
		out[name] = n
	}
	a, err := quark.For[domain.Account](ctx, client).Count()
	count("accounts", a, err)
	p, err := quark.For[domain.Project](ctx, client).Count()
	count("projects", p, err)
	t, err := quark.For[domain.Task](ctx, client).Count()
	count("tasks", t, err)
	g, err := quark.For[domain.Tag](ctx, client).Count()
	count("tags", g, err)
	m, err := quark.For[domain.Membership](ctx, client).Count()
	count("memberships", m, err)
	at, err := quark.For[domain.Attachment](ctx, client).Count()
	count("attachments", at, err)
	return out, fails
}

// sqlVerb devuelve el verbo SQL inicial en mayúsculas (SELECT/INSERT/UPDATE/…).
func sqlVerb(sql string) string {
	sql = strings.TrimLeft(sql, " \t\n\r(")
	i := strings.IndexAny(sql, " \t\n\r(")
	if i < 0 {
		i = len(sql)
	}
	return strings.ToUpper(sql[:i])
}

// statOf calcula percentiles (ms) de un conjunto de duraciones. Método de índice
// más cercano sobre los datos ordenados (idx = round(p·(n-1))); para n par, p50
// cae en la mitad superior. Resolución completa (ns) para no truncar queries
// sub-microsegundo.
func statOf(ds []time.Duration) LatencyStat {
	if len(ds) == 0 {
		return LatencyStat{}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	ms := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }
	pick := func(p float64) float64 {
		idx := int(p*float64(len(ds)-1) + 0.5)
		if idx < 0 {
			idx = 0
		}
		if idx >= len(ds) {
			idx = len(ds) - 1
		}
		return ms(ds[idx])
	}
	return LatencyStat{
		Count: len(ds), P50: pick(0.50), P95: pick(0.95), P99: pick(0.99), Max: ms(ds[len(ds)-1]),
	}
}

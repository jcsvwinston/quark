package exercise

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// sptDoc vive una vez POR SCHEMA de tenant — no lleva tenant_id: bajo
// SchemaPerTenant el aislamiento es la qualificación schema.table que el builder
// emite con q.schema = tenantID (client.go:385). Tabla propia del exerciser para
// que los asserts sobre rec.Statements() no casen con statements de otros.
type sptDoc struct {
	ID    int64  `db:"id" pk:"true"`
	Title string `db:"title"`
}

func (sptDoc) TableName() string { return "spt_docs" }

// Los nombres de schema son a la vez los tenant IDs (validTenantID exige
// ^[a-z0-9_-]+$). Fijos + cleanup a la entrada y salida → re-ejecutable contra
// un contenedor persistente.
const (
	sptSchemaA = "superapp_spt_ta"
	sptSchemaB = "superapp_spt_tb"
)

// SCHEMAPERTENANT ejerce la estrategia SchemaPerTenant (ADR-0007): una base, un
// schema por tenant; For[T] bajo el router fija q.schema = tenantID y todo el SQL
// sale schema-qualified. Se asertan las dos garantías:
//
//  1. Aislamiento por schema — cada tenant ve sólo las filas de SU schema
//     (tablas físicamente distintas dentro de la misma base).
//  2. La qualificación llega al SQL EMITIDO, incluida la regresión BB-8 (los
//     write-paths construían BaseQuery internos que perdían q.schema y los
//     INSERT caían al schema default): se inspecciona rec.Statements() y se
//     exige que el INSERT mencione el schema del tenant.
//
// Sólo corre el path funcional donde el motor tiene schemas reales
// (FeatSchemaPerTenant: PG y MSSQL — fuente docs/playbooks/tenant.md); en el
// resto salta limpio (Quark NO gatea esta estrategia con ErrUnsupportedFeature,
// así que no hay error que asertar — ver el comment en capability.go). El
// onboarding (CREATE SCHEMA + migrar la tabla al schema) es responsabilidad del
// caller per el playbook ("SchemaPerTenant no auto-crea schema"): aquí el admin
// crea los schemas y un client efímero con search_path=<schema> migra dentro de
// cada uno — en MSSQL no existe el equivalente de search_path en DSN, así que su
// mecanismo de migrate-into-schema queda TODO (error ruidoso, no skip silencioso).
var SCHEMAPERTENANT = Exerciser{Name: "tenant-schema-per", Fn: runSchemaPerTenant}

func runSchemaPerTenant(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	rec.Note(QF("SchemaPerTenant"), QF("NewTenantRouter"), QF("DefaultTenantConfig"),
		QF("For"), TRM("GetClient"))

	if !control.Supports(control.FeatSchemaPerTenant, conn.Engine) {
		return nil // sin schemas reales: skip documentado (capability.go)
	}
	if conn.Engine != control.Postgres {
		// MSSQL soporta schemas (la capability lo dice) pero el harness aún no
		// tiene cómo migrar la tabla DENTRO de un schema ahí (no hay search_path
		// por DSN). Error ruidoso para que habilitar MSSQL en la matriz obligue a
		// implementarlo — no un skip que infle la cobertura.
		return fmt.Errorf("schema-per-tenant: falta el mecanismo de migrate-into-schema para %s (TODO en HANDOFF)", conn.Engine)
	}

	admin, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(quark.Limits{
		AllowRawQueries: true,
		MaxResults:      1000,
		QueryTimeout:    30 * time.Second,
	}))
	if err != nil {
		return fmt.Errorf("admin client: %w", err)
	}
	defer admin.Close() // LIFO: el último — el cleanup necesita el admin abierto

	cleanup := func() {
		cctx := context.Background()
		_ = admin.Exec(cctx, `DROP SCHEMA IF EXISTS `+sptSchemaA+` CASCADE`)
		_ = admin.Exec(cctx, `DROP SCHEMA IF EXISTS `+sptSchemaB+` CASCADE`)
	}
	cleanup()
	defer cleanup()

	// Onboarding por tenant: CREATE SCHEMA + migrar la tabla DENTRO del schema
	// vía un client efímero cuyo DSN fija search_path=<schema> (pgx pasa los
	// query-params desconocidos como runtime params de la sesión).
	for _, schema := range []string{sptSchemaA, sptSchemaB} {
		if err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
			return fmt.Errorf("create schema %s: %w", schema, err)
		}
		dsn, err := searchPathDSN(conn.DSN, schema)
		if err != nil {
			return err
		}
		l := quark.DefaultLimits()
		l.SafeMigrations = false
		tmp, err := quark.New(conn.Driver, dsn, quark.WithLimits(l))
		if err != nil {
			return fmt.Errorf("client search_path=%s: %w", schema, err)
		}
		merr := tmp.Migrate(ctx, &sptDoc{})
		// El error de Close se descarta a sabiendas: si la conexión quedara viva,
		// el leak-check de engine.Run (goroutines + pool) lo delataría igualmente.
		_ = tmp.Close()
		if merr != nil {
			return fmt.Errorf("migrate en schema %s: %w", schema, merr)
		}
	}

	// El router enruta sobre el client del harness (instrumentado por el
	// recorder → el SQL de tenant queda capturado para el assert BB-8).
	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.SchemaPerTenant
	cfg.BaseClient = client
	router := quark.NewTenantRouter(cfg, tenantResolver, nil)

	ctxA, ctxB := tenantCtx(sptSchemaA), tenantCtx(sptSchemaB)

	// --- 1) Escrituras por tenant: cada INSERT cae en SU schema ---
	for _, title := range []string{"spt-a-1", "spt-a-2"} {
		if err := quark.For[sptDoc](rec.Mark(ctxA, QM("Create")), router).Create(&sptDoc{Title: title}); err != nil {
			return fmt.Errorf("create %s %q: %w", sptSchemaA, title, err)
		}
	}
	if err := quark.For[sptDoc](rec.Mark(ctxB, QM("Create")), router).Create(&sptDoc{Title: "spt-b-1"}); err != nil {
		return fmt.Errorf("create %s: %w", sptSchemaB, err)
	}

	// --- 2) BB-8: el INSERT emitido va schema-qualified ---
	// Sin la qualificación, el statement no mencionaría el schema en absoluto
	// (caería al default y los tenants se co-mingarían). Match laxo (contiene
	// ambos nombres) para no atarse al estilo de quoting del dialecto.
	found := false
	for _, st := range rec.Statements() {
		up := strings.ToUpper(st.SQL)
		if strings.HasPrefix(up, "INSERT") &&
			strings.Contains(st.SQL, sptSchemaA) && strings.Contains(st.SQL, "spt_docs") {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("BB-8: ningún INSERT capturado menciona %s.spt_docs (¿write-path perdió q.schema?)", sptSchemaA)
	}

	// --- 3) Aislamiento: cada tenant lee sólo su schema ---
	aDocs, err := quark.For[sptDoc](rec.Mark(ctxA, QM("List")), router).List()
	if err != nil {
		return fmt.Errorf("list %s: %w", sptSchemaA, err)
	}
	if len(aDocs) != 2 {
		return fmt.Errorf("%s: esperaba 2 filas en su schema, obtuve %d", sptSchemaA, len(aDocs))
	}
	bDocs, err := quark.For[sptDoc](rec.Mark(ctxB, QM("List")), router).List()
	if err != nil {
		return fmt.Errorf("list %s: %w", sptSchemaB, err)
	}
	if len(bDocs) != 1 || bDocs[0].Title != "spt-b-1" {
		return fmt.Errorf("%s: esperaba exactamente su fila spt-b-1, obtuve %+v", sptSchemaB, bDocs)
	}

	// --- 4) Cross-schema: la fila de ta no existe bajo el schema de tb ---
	ghost, err := quark.For[sptDoc](rec.Mark(ctxB, QM("Where")), router).
		Where("id", "=", aDocs[0].ID).Where("title", "=", aDocs[0].Title).List()
	if err != nil {
		return fmt.Errorf("cross-schema probe: %w", err)
	}
	if len(ghost) != 0 {
		return fmt.Errorf("FUGA de schema: %s ve una fila de %s (%+v)", sptSchemaB, sptSchemaA, ghost)
	}
	return nil
}

// searchPathDSN añade search_path=<schema> a un DSN Postgres URL-form: el client
// resultante crea/inspecciona tablas dentro de ese schema, que es como el caller
// onboardea un tenant (el playbook: las migraciones por schema son suyas).
func searchPathDSN(dsn, schema string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("search_path necesita DSN Postgres URL-form, obtuve %q", dsn)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

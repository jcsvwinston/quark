package exercise

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// tenantDoc es un modelo tenant-scoped (los modelos del dominio no lo son).
// Local al exerciser, igual que el testMultiTenant del propio repo. La columna
// tenant_id casa con el TenantColumn por defecto ("tenant_id").
type tenantDoc struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Title    string `db:"title"`
}

func (tenantDoc) TableName() string { return "tenant_docs" }

type tenantCtxKey string

const tenantKey tenantCtxKey = "tenant_id"

func tenantCtx(tid string) context.Context {
	return context.WithValue(context.Background(), tenantKey, tid)
}

func tenantResolver(c context.Context) string {
	if v, ok := c.Value(tenantKey).(string); ok {
		return v
	}
	return ""
}

// TENANT ejerce la modalidad RowLevelSecurityClient de multi-tenancy (ADR-0007):
// inyección de `WHERE tenant_id = ?` en el builder, disponible en los 6 motores.
// Asierta la garantía de seguridad crítica — aislamiento cross-tenant — y las
// trampas que el playbook marca: la propagación del predicado a los Or-groups
// (regresión del P0-1) y que el aislamiento sólo aplica A TRAVÉS del router (una
// query con el client base, igual que `client.Raw()`/`Exec()`, lo evita).
//
// Es builder-only a propósito (sin SQL raw): así corre portable en los 6 motores
// sin tropezar con el case de identificadores de Oracle.
//
// Las otras 3 estrategias necesitan fixtures más pesados y llegan en PRs propios
// (ver examples/superapp/HANDOFF.md): RowLevelSecurityNative (PG-only, requiere
// un rol no-superuser + CREATE POLICY), SchemaPerTenant (PG/MSSQL, CREATE SCHEMA)
// y DatabasePerTenant (factory de *Client por tenant con DSN propio).
var TENANT = Exerciser{Name: "tenant", Fn: runTenant}

func runTenant(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	rec.Note(QF("NewTenantRouter"), QF("DefaultTenantConfig"), QF("TenantConfig"),
		QF("TenantRouter"), QF("TenantStrategy"), QF("RowLevelSecurityClient"),
		TRM("ResolveTenant"), TRM("GetClient"), QM("Or"), QM("Where"))

	if err := client.Migrate(rec.Mark(ctx, CM("Migrate")), &tenantDoc{}); err != nil {
		return fmt.Errorf("migrate tenant_docs: %w", err)
	}

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityClient
	cfg.BaseClient = client
	// factory nil: RowLevelSecurityClient reusa el BaseClient (no necesita pool
	// por tenant).
	router := quark.NewTenantRouter(cfg, tenantResolver, nil)

	ctxA := tenantCtx("ta")
	ctxB := tenantCtx("tb")

	// --- 1) Escritura tenant-scoped: el router inyecta tenant_id en el INSERT ---
	for _, title := range []string{"a-doc-1", "a-doc-2"} {
		if err := quark.For[tenantDoc](rec.Mark(ctxA, QM("Create")), router).Create(&tenantDoc{Title: title}); err != nil {
			return fmt.Errorf("create ta %q: %w", title, err)
		}
	}
	if err := quark.For[tenantDoc](rec.Mark(ctxB, QM("Create")), router).Create(&tenantDoc{Title: "b-doc-1"}); err != nil {
		return fmt.Errorf("create tb: %w", err)
	}

	// --- 2) Aislamiento: cada tenant ve SÓLO sus filas ---
	aDocs, err := quark.For[tenantDoc](rec.Mark(ctxA, QM("List")), router).List()
	if err != nil {
		return fmt.Errorf("list ta: %w", err)
	}
	if len(aDocs) != 2 {
		return fmt.Errorf("aislamiento ta: esperaba 2 filas, obtuve %d", len(aDocs))
	}
	for _, d := range aDocs {
		if d.TenantID != "ta" {
			return fmt.Errorf("FUGA: ta vio una fila del tenant %q", d.TenantID)
		}
	}
	bDocs, err := quark.For[tenantDoc](rec.Mark(ctxB, QM("List")), router).List()
	if err != nil {
		return fmt.Errorf("list tb: %w", err)
	}
	if len(bDocs) != 1 || bDocs[0].TenantID != "tb" {
		return fmt.Errorf("aislamiento tb: esperaba 1 fila de tb, obtuve %d (%+v)", len(bDocs), bDocs)
	}

	// --- 3) El predicado tenant se propaga al Or-group (regresión del P0-1) ---
	// ta busca a-doc-1 OR el título de una fila de tb: el aislamiento debe
	// impedir que la rama Or se cuele filas de otro tenant.
	orDocs, err := quark.For[tenantDoc](rec.Mark(ctxA, QM("List")), router).
		Where("title", "=", "a-doc-1").
		Or(func(q *quark.Query[tenantDoc]) *quark.Query[tenantDoc] {
			return q.Where("title", "=", "b-doc-1")
		}).List()
	if err != nil {
		return fmt.Errorf("or-group ta: %w", err)
	}
	for _, d := range orDocs {
		if d.TenantID != "ta" {
			return fmt.Errorf("FUGA en Or-group: ta vio una fila del tenant %q (el predicado tenant no se propagó al grupo)", d.TenantID)
		}
	}

	// --- 4) El aislamiento es del ROUTER: el client base ve todo ---
	// No es un bug: RLSClient es WHERE-injection del builder. Una query con el
	// client base (como client.Raw()/Exec(), per el playbook) lo evita. Lo
	// asertamos para que la limitación quede cubierta y visible.
	all, err := quark.For[tenantDoc](rec.Mark(context.Background(), QM("List")), client).List()
	if err != nil {
		return fmt.Errorf("list base client: %w", err)
	}
	if len(all) != 3 {
		return fmt.Errorf("el client base (sin router) debería ver las 3 filas (sin aislar), obtuve %d", len(all))
	}

	// --- 5) tenant_id inválido (regex) / ausente → ResolveTenant rechaza ---
	if _, err := quark.For[tenantDoc](tenantCtx("BAD ID!"), router).List(); err == nil {
		return errors.New("un tenant_id inválido (regex ^[a-z0-9_-]+$) debió ser rechazado")
	}
	if _, err := quark.For[tenantDoc](context.Background(), router).List(); err == nil {
		return errors.New("un context sin tenant_id debió ser rechazado")
	}
	return nil
}

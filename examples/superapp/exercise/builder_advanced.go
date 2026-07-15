package exercise

import (
	"context"
	"errors"
	"fmt"
	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// BUILDERADV ejerce los ~35 métodos de Query[T] que el builder común no
// cubre: scopes de soft-delete (WithTrashed/OnlyTrashed/Unscoped/Restore/
// HardDelete), variantes de WHERE (Between/Not/P/Expr + Apply), dirty
// tracking (Track→Find→Save, que SÍ escribe zero-values — el cierre de
// P0-4), UpdateFields, joins estructurados (LeftJoin/RightJoin + On/OnRaw),
// HAVING (HavingAggregate/HavingExpr), window functions (SelectExpr +
// Over/RowNumber), subqueries (AsSubquery/MustAsSubquery/WhereSubquery —
// este último gateado por AllowRawQueries: se asierta el RECHAZO en el
// client del harness y el camino feliz en uno flaggeado), CTEs
// (With/WithRecursive), set operators (Union/UnionAll/Intersect/Except),
// locking pesimista por capability (ForUpdate/ForShare/NoWait/SkipLocked —
// ErrUnsupportedFeature en SQLite, camino real en tx en los servidores), y
// el CRUD por lotes restante (DeleteBy/DeleteBatch/UpdateBatch/Upsert/
// UpsertBatch — OJO: UpsertBatch no chunkea, deuda trackeada en el playbook;
// lotes pequeños a propósito).
//
// Todas las filas-sonda llevan el marcador "badv-" y se eliminan al salir
// (HardDelete/DeleteBy): los counts van SIEMPRE scoped al marcador para no
// interferir con el residuo de otros exercisers en las tablas del dominio.
var BUILDERADV = Exerciser{Name: "builder-advanced", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	mail := func(i int) string { return fmt.Sprintf("badv-%02d@superapp.test", i) }
	scopedAcc := func(c context.Context) *quark.Query[domain.Account] {
		return quark.For[domain.Account](c, client).Where("email", "LIKE", "badv-%")
	}

	// --- Seed: 4 cuentas marcadas + 2 proyectos para los joins. -------------
	accs := make([]*domain.Account, 4)
	for i := range accs {
		accs[i] = &domain.Account{Email: mail(i), Name: fmt.Sprintf("badv%d", i), Role: "member", Active: true}
	}
	accs[3].Role = "admin"
	if err := quark.For[domain.Account](ctx, client).CreateBatch(accs); err != nil {
		return fmt.Errorf("seed cuentas: %w", err)
	}
	cleanup := func() {
		cctx := context.Background()
		_, _ = quark.For[domain.Project](cctx, client).Where("name", "LIKE", "badv-%").DeleteBy()
		rows, _ := quark.For[domain.Account](cctx, client).Where("email", "LIKE", "badv-%").WithTrashed().List()
		for i := range rows {
			_, _ = quark.For[domain.Account](cctx, client).HardDelete(&rows[i])
		}
	}
	cleanupRegistered := false
	defer func() {
		if !cleanupRegistered {
			cleanup()
		}
	}()
	for _, p := range []*domain.Project{
		{OwnerID: accs[0].ID, Name: "badv-p0", Status: "draft"},
		{OwnerID: accs[1].ID, Name: "badv-p1", Status: "active"},
	} {
		if err := quark.For[domain.Project](ctx, client).Create(p); err != nil {
			return fmt.Errorf("seed proyecto: %w", err)
		}
	}

	// --- 1. Variantes de WHERE + Apply + UpdateFields + Track. --------------
	if n, err := scopedAcc(rec.Mark(ctx, QM("WhereBetween"))).WhereBetween("id", accs[0].ID, accs[3].ID).Count(); err != nil || n != 4 {
		return fmt.Errorf("WhereBetween: n=%d err=%v, esperaba 4", n, err)
	}
	if n, err := scopedAcc(rec.Mark(ctx, QM("WhereNot"))).WhereNot("role", "=", "admin").Count(); err != nil || n != 3 {
		return fmt.Errorf("WhereNot: n=%d err=%v, esperaba 3", n, err)
	}
	// WhereP con columnas tipadas construidas a mano (NewTypedColumn): la vía
	// sin codegen — el SQL es el mismo que emitiría el Where string (F9 pinneó
	// la paridad byte-a-byte con las columnas generadas).
	rec.Note(QF("NewTypedColumn"), QF("Predicate"))
	role := quark.NewTypedColumn[string]("role")
	if n, err := scopedAcc(rec.Mark(ctx, QM("WhereP"))).WhereP(role.Eq("admin")).Count(); err != nil || n != 1 {
		return fmt.Errorf("WhereP: n=%d err=%v, esperaba 1", n, err)
	}
	// WhereExpr con el AST de expresiones.
	rec.Note(QF("Col"), QF("Lit"), QF("Eq"))
	if n, err := scopedAcc(rec.Mark(ctx, QM("WhereExpr"))).WhereExpr(quark.Eq(quark.Col("role"), quark.Lit("admin"))).Count(); err != nil || n != 1 {
		return fmt.Errorf("WhereExpr: n=%d err=%v, esperaba 1", n, err)
	}
	// Apply composa scopes reutilizables.
	rec.Note(QF("Scope"))
	admins := func(q *quark.Query[domain.Account]) *quark.Query[domain.Account] {
		return q.Where("role", "=", "admin")
	}
	if n, err := scopedAcc(rec.Mark(ctx, QM("Apply"))).Apply(admins).Count(); err != nil || n != 1 {
		return fmt.Errorf("Apply: n=%d err=%v, esperaba 1", n, err)
	}
	// UpdateFields escribe EXACTAMENTE los campos pedidos (incl. zero-values).
	accs[2].Active = false
	if rows, err := quark.For[domain.Account](rec.Mark(ctx, QM("UpdateFields")), client).UpdateFields(accs[2], "active"); err != nil || rows != 1 {
		return fmt.Errorf("UpdateFields: rows=%d err=%v", rows, err)
	}
	if got, err := scopedAcc(ctx).Where("id", "=", accs[2].ID).First(); err != nil || got.Active {
		return fmt.Errorf("UpdateFields no escribió el zero-value Active=false (got %+v, err=%v)", got.Active, err)
	}
	// Track → Find → mutar → Save: el diff por snapshot escribe zero-values y
	// un re-Save sin cambios no emite SQL.
	rec.Note(QF("(*TrackedQuery[T]).Find"), QF("(*Tracked[T]).Save"), QF("(*Tracked[T]).Changed"))
	tracked, err := quark.For[domain.Account](rec.Mark(ctx, QM("Track")), client).Track().Find(accs[1].ID)
	if err != nil {
		return fmt.Errorf("Track.Find: %w", err)
	}
	tracked.Entity.Name = "badv-tracked"
	if len(tracked.Changed()) == 0 {
		return fmt.Errorf("Tracked.Changed vacío tras mutar")
	}
	if rows, err := tracked.Save(ctx); err != nil || rows != 1 {
		return fmt.Errorf("Tracked.Save: rows=%d err=%v", rows, err)
	}
	// OJO: el contrato "sin cambios → sin SQL" NO aplica a Account — Save
	// corre BeforeUpdate ANTES del diff (dirty_track.go) y el hook toca
	// UpdatedAt en cada llamada, así que siempre hay delta. El contrato puro
	// lo pinnea dirty_track_test.go con un modelo sin hooks; aquí se asierta
	// que el re-Save es válido e idempotente a nivel de datos.
	if rows, err := tracked.Save(ctx); err != nil || rows != 1 {
		return fmt.Errorf("re-Save: rows=%d err=%v (el hook garantiza delta)", rows, err)
	}

	// --- 2. Joins estructurados. ---------------------------------------------
	// El ON acepta identificadores cualificados (grammar de ValidateJoinOn);
	// Where/Select del builder NO — las columnas del modelo se emiten ya
	// cualificadas por quark en presencia de JOIN (patrón de cte_test.go).
	rec.Note(QF("(*JoinBuilder[T]).On"))
	withProj, err := quark.For[domain.Account](rec.Mark(ctx, QM("LeftJoin")), client).
		Where("email", "LIKE", "badv-%").
		LeftJoin("projects").On("projects.owner_id", "=", "accounts.id").
		Limit(10).List()
	if err != nil {
		return fmt.Errorf("LeftJoin: %w", err)
	}
	if len(withProj) != 4 {
		return fmt.Errorf("LeftJoin: %d filas, esperaba 4 (conserva cuentas sin proyecto; a0/a1 tienen 1 cada una)", len(withProj))
	}
	// RIGHT JOIN: preserva el lado derecho (projects); el WHERE sobre la
	// izquierda lo reduce a los pares casados — 2 proyectos badv. Requiere
	// SQLite ≥3.39 (modernc actual lo cumple; mattn con libsqlite3 vieja no).
	rec.Note(QF("(*JoinBuilder[T]).OnRaw"))
	rj, err := quark.For[domain.Account](rec.Mark(ctx, QM("RightJoin")), client).
		Where("email", "LIKE", "badv-%").
		RightJoin("projects").OnRaw("projects.owner_id = accounts.id").
		Limit(10).List()
	if err != nil {
		return fmt.Errorf("RightJoin: %w", err)
	}
	if len(rj) != 2 {
		return fmt.Errorf("RightJoin: %d filas, esperaba 2 (los proyectos badv con su owner)", len(rj))
	}

	// --- 3. HAVING + window functions. ---------------------------------------
	if _, err := scopedAcc(rec.Mark(ctx, QM("HavingAggregate"))).
		Select("role").GroupBy("role").HavingAggregate("COUNT", "id", ">=", 1).Limit(10).List(); err != nil {
		return fmt.Errorf("HavingAggregate: %w", err)
	}
	rec.Note(QF("Gte"))
	if _, err := scopedAcc(rec.Mark(ctx, QM("HavingExpr"))).
		Select("role").GroupBy("role").HavingExpr(quark.Gte(quark.Col("role"), quark.Lit("a"))).Limit(10).List(); err != nil {
		return fmt.Errorf("HavingExpr: %w", err)
	}
	rec.Note(QF("NewWindow"), QF("Over"), QF("RowNumber"), QF("(*Window).PartitionBy"), QF("(*Window).OrderBy"))
	w := quark.NewWindow().PartitionBy(quark.Col("role")).OrderBy(quark.Col("id"), false)
	if _, err := scopedAcc(rec.Mark(ctx, QM("SelectExpr"))).
		Select("id").SelectExpr("rn", quark.Over(quark.RowNumber(), w)).Limit(10).List(); err != nil {
		return fmt.Errorf("SelectExpr+window: %w", err)
	}

	// --- 4. Subqueries + CTEs. ------------------------------------------------
	rec.Note(QF("Subquery"))
	sub, err := quark.For[domain.Account](rec.Mark(ctx, QM("AsSubquery")), client).
		Select("id").Where("email", "LIKE", "badv-%").AsSubquery()
	if err != nil {
		return fmt.Errorf("AsSubquery: %w", err)
	}
	_ = quark.For[domain.Account](rec.Mark(ctx, QM("MustAsSubquery")), client).
		Select("id").Where("role", "=", "admin").MustAsSubquery()
	// CTE: el WITH se referencia por nombre vía Join.
	cte, err := quark.For[domain.Account](rec.Mark(ctx, QM("With")), client).
		With("badv_ids", sub).
		Join("badv_ids").On("accounts.id", "=", "badv_ids.id").
		Limit(10).List()
	if err != nil {
		return fmt.Errorf("With (CTE): %w", err)
	}
	if len(cte) != 4 {
		return fmt.Errorf("CTE: %d filas, esperaba 4", len(cte))
	}
	// WithRecursive: la forma sintáctica (WITH RECURSIVE) sobre un sub no
	// recursivo es válida en los 6 — el shape recursivo real está pinneado en
	// cte_test.go; aquí ejercemos el método y el render.
	if _, err := quark.For[domain.Account](rec.Mark(ctx, QM("WithRecursive")), client).
		WithRecursive("badv_rec", sub).
		Join("badv_rec").On("accounts.id", "=", "badv_rec.id").
		Limit(10).List(); err != nil {
		return fmt.Errorf("WithRecursive: %w", err)
	}
	// WhereSubquery está gateado por AllowRawQueries: el client del harness lo
	// RECHAZA (postura de seguridad por defecto)…
	if _, err := scopedAcc(rec.Mark(ctx, QM("WhereSubquery"))).
		WhereSubquery("id", "IN", "SELECT id FROM accounts").Count(); !errors.Is(err, quark.ErrInvalidQuery) {
		return fmt.Errorf("WhereSubquery sin AllowRawQueries: esperaba ErrInvalidQuery, got %v", err)
	}
	// …y funciona en un client con el flag (mismo patrón que migrations.mdx).
	lraw := quark.DefaultLimits()
	lraw.AllowRawQueries = true
	rawClient, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(lraw))
	if err != nil {
		return fmt.Errorf("client raw: %w", err)
	}
	defer rawClient.Close()
	if n, err := quark.For[domain.Account](ctx, rawClient).Where("email", "LIKE", "badv-%").
		WhereSubquery("id", "IN", "SELECT id FROM accounts WHERE role = 'admin'").Count(); err != nil || n != 1 {
		return fmt.Errorf("WhereSubquery con flag: n=%d err=%v, esperaba 1", n, err)
	}

	// --- 5. Set operators. ----------------------------------------------------
	admins2 := quark.For[domain.Account](ctx, client).Select("email").Where("email", "LIKE", "badv-%").Where("role", "=", "admin")
	members := quark.For[domain.Account](ctx, client).Select("email").Where("email", "LIKE", "badv-%").Where("role", "=", "member")
	all := quark.For[domain.Account](ctx, client).Select("email").Where("email", "LIKE", "badv-%")
	if rows, err := admins2.Union(members).Limit(10).List(); err != nil {
		return fmt.Errorf("Union: %w", err)
	} else if len(rows) != 4 {
		return fmt.Errorf("Union: %d filas, esperaba 4 (1 admin + 3 members)", len(rows))
	}
	rec.Note(QM("Union"))
	if rows, err := admins2.UnionAll(admins2).Limit(10).List(); err != nil {
		return fmt.Errorf("UnionAll: %w", err)
	} else if len(rows) != 2 {
		return fmt.Errorf("UnionAll: %d filas, esperaba 2 (duplicados conservados)", len(rows))
	}
	rec.Note(QM("UnionAll"))
	// INTERSECT/EXCEPT: feature gateada por Quark (setop.go) — MySQL devuelve
	// ErrUnsupportedFeature (8.0.31+ no asumible); se asierta el sentinel ahí.
	// MariaDB (10.3+) los ejecuta desde QK-P2-2.
	if control.Supports(control.FeatIntersectExcept, conn.Engine) {
		if rows, err := all.Intersect(admins2).Limit(10).List(); err != nil {
			return fmt.Errorf("Intersect: %w", err)
		} else if len(rows) != 1 {
			return fmt.Errorf("Intersect: %d filas, esperaba 1", len(rows))
		}
		if rows, err := all.Except(admins2).Limit(10).List(); err != nil {
			return fmt.Errorf("Except: %w", err)
		} else if len(rows) != 3 {
			return fmt.Errorf("Except: %d filas, esperaba 3", len(rows))
		}
	} else {
		if _, err := all.Intersect(admins2).Limit(10).List(); !errors.Is(err, quark.ErrUnsupportedFeature) {
			return fmt.Errorf("Intersect en %s: esperaba ErrUnsupportedFeature, got %v", conn.Engine, err)
		}
		if _, err := all.Except(admins2).Limit(10).List(); !errors.Is(err, quark.ErrUnsupportedFeature) {
			return fmt.Errorf("Except en %s: esperaba ErrUnsupportedFeature, got %v", conn.Engine, err)
		}
	}
	rec.Note(QM("Intersect"), QM("Except"))
	// INTERSECT ALL / EXCEPT ALL: soporte más estrecho (sólo PG y MariaDB); en
	// el resto — incluido MSSQL/Oracle, que sí tienen las variantes distinct —
	// Quark devuelve ErrUnsupportedFeature y se asierta el sentinel.
	if control.Supports(control.FeatIntersectExceptAll, conn.Engine) {
		if _, err := all.IntersectAll(admins2).Limit(10).List(); err != nil {
			return fmt.Errorf("IntersectAll: %w", err)
		}
		if _, err := all.ExceptAll(admins2).Limit(10).List(); err != nil {
			return fmt.Errorf("ExceptAll: %w", err)
		}
	} else {
		if _, err := all.IntersectAll(admins2).Limit(10).List(); !errors.Is(err, quark.ErrUnsupportedFeature) {
			return fmt.Errorf("IntersectAll en %s: esperaba ErrUnsupportedFeature, got %v", conn.Engine, err)
		}
		if _, err := all.ExceptAll(admins2).Limit(10).List(); !errors.Is(err, quark.ErrUnsupportedFeature) {
			return fmt.Errorf("ExceptAll en %s: esperaba ErrUnsupportedFeature, got %v", conn.Engine, err)
		}
	}
	rec.Note(QM("IntersectAll"), QM("ExceptAll"))

	// --- 6. Locking pesimista, por capability. --------------------------------
	if control.Supports(control.FeatSkipLocked, conn.Engine) {
		// Oracle no admite combinar FOR UPDATE/SKIP LOCKED/NOWAIT con un Limit/Offset
		// explícito (ORA-02014; ver BB-4 + suppressRowLimit en query_exec.go). Ahí el
		// lock se ejerce SIN Limit (bloquea todas las filas badv-%, pocas y dentro de
		// la tx que revierte); el resto de motores combinan lock + Limit(2).
		lockLimit := func(q *quark.Query[domain.Account]) *quark.Query[domain.Account] {
			if conn.Engine == control.Oracle {
				return q
			}
			return q.Limit(2)
		}
		// Camino real: dentro de una tx, los cuatro modificadores ejecutan.
		if err := client.Tx(ctx, func(tx *quark.Tx) error {
			if _, err := lockLimit(quark.ForTx[domain.Account](rec.Mark(ctx, QM("ForUpdate")), tx).
				Where("email", "LIKE", "badv-%").ForUpdate()).List(); err != nil {
				return fmt.Errorf("ForUpdate: %w", err)
			}
			if _, err := lockLimit(quark.ForTx[domain.Account](rec.Mark(ctx, QM("SkipLocked")), tx).
				Where("email", "LIKE", "badv-%").ForUpdate().SkipLocked()).List(); err != nil {
				return fmt.Errorf("SkipLocked: %w", err)
			}
			if _, err := lockLimit(quark.ForTx[domain.Account](rec.Mark(ctx, QM("NoWait")), tx).
				Where("email", "LIKE", "badv-%").ForUpdate().NoWait()).List(); err != nil {
				// MSSQL has no NOWAIT for table hints (it uses SET LOCK_TIMEOUT 0);
				// Quark rejects it at build time with ErrUnsupportedFeature, before
				// any SQL — so the tx isn't poisoned and we continue. Capacidad
				// desigual ≠ fallo (mismo trato que ForShare abajo).
				if !errors.Is(err, quark.ErrUnsupportedFeature) {
					return fmt.Errorf("NoWait: %w", err)
				}
			}
			if _, err := lockLimit(quark.ForTx[domain.Account](rec.Mark(ctx, QM("ForShare")), tx).
				Where("email", "LIKE", "badv-%").ForShare()).List(); err != nil {
				// MSSQL no modela FOR SHARE: capacidad desigual ≠ fallo.
				if errors.Is(err, quark.ErrUnsupportedFeature) {
					return nil
				}
				return fmt.Errorf("ForShare: %w", err)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("locking tx: %w", err)
		}
	} else {
		// SQLite no modela locks de fila: el builder acepta el modificador y la
		// ejecución devuelve ErrUnsupportedFeature sin emitir SQL.
		if _, err := scopedAcc(rec.Mark(ctx, QM("ForUpdate"))).ForUpdate().Limit(1).List(); !errors.Is(err, quark.ErrUnsupportedFeature) {
			return fmt.Errorf("ForUpdate en %s: esperaba ErrUnsupportedFeature, got %v", conn.Engine, err)
		}
		rec.Note(QM("ForShare"), QM("SkipLocked"), QM("NoWait"))
	}

	// --- 7. Soft-delete scopes + Upsert + CRUD por lotes. ----------------------
	// Soft-delete de una cuenta → los scopes la ven distinto.
	victim, err := scopedAcc(ctx).Where("id", "=", accs[0].ID).First()
	if err != nil {
		return fmt.Errorf("first pre-soft-delete: %w", err)
	}
	if _, err := quark.For[domain.Account](ctx, client).Delete(&victim); err != nil {
		return fmt.Errorf("soft-delete: %w", err)
	}
	if n, _ := scopedAcc(ctx).Count(); n != 3 {
		return fmt.Errorf("default scope: %d, esperaba 3 (excluye la borrada)", n)
	}
	if n, _ := scopedAcc(rec.Mark(ctx, QM("WithTrashed"))).WithTrashed().Count(); n != 4 {
		return fmt.Errorf("WithTrashed: esperaba 4")
	}
	if n, _ := scopedAcc(rec.Mark(ctx, QM("OnlyTrashed"))).OnlyTrashed().Count(); n != 1 {
		return fmt.Errorf("OnlyTrashed: esperaba 1")
	}
	if n, _ := scopedAcc(rec.Mark(ctx, QM("Unscoped"))).Unscoped().Count(); n != 4 {
		return fmt.Errorf("Unscoped: esperaba 4")
	}
	// Restore la revive.
	trashed, err := scopedAcc(ctx).OnlyTrashed().First()
	if err != nil {
		return fmt.Errorf("first trashed: %w", err)
	}
	if rows, err := quark.For[domain.Account](rec.Mark(ctx, QM("Restore")), client).Restore(&trashed); err != nil || rows != 1 {
		return fmt.Errorf("Restore: rows=%d err=%v", rows, err)
	}
	if n, _ := scopedAcc(ctx).Count(); n != 4 {
		return fmt.Errorf("post-Restore: esperaba 4 visibles")
	}
	// Upsert: conflicto por email → actualiza name.
	up := &domain.Account{Email: mail(0), Name: "badv-upserted", Role: "member", Active: true}
	if err := quark.For[domain.Account](rec.Mark(ctx, QM("Upsert")), client).Upsert(up, []string{"email"}, []string{"name"}); err != nil {
		return fmt.Errorf("Upsert: %w", err)
	}
	if got, err := scopedAcc(ctx).Where("email", "=", mail(0)).First(); err != nil || got.Name != "badv-upserted" {
		return fmt.Errorf("Upsert no actualizó por conflicto: name=%q err=%v", got.Name, err)
	}
	// UpsertBatch (SIN chunking — deuda del playbook: lote pequeño a propósito).
	batch := []*domain.Account{
		{Email: mail(1), Name: "badv-ub1", Role: "member", Active: true},
		// Inserta una cuenta NUEVA (badv-09) — el cleanup la cubre porque
		// barre por prefijo de email, no por la lista del seed.
		{Email: mail(9), Name: "badv-new", Role: "viewer", Active: true},
	}
	if err := quark.For[domain.Account](rec.Mark(ctx, QM("UpsertBatch")), client).UpsertBatch(batch, []string{"email"}, []string{"name"}); err != nil {
		return fmt.Errorf("UpsertBatch: %w", err)
	}
	if n, _ := scopedAcc(ctx).Count(); n != 5 {
		return fmt.Errorf("post-UpsertBatch: esperaba 5 (1 update + 1 insert)")
	}
	// UpdateBatch sobre dos entidades.
	rows, err := scopedAcc(ctx).Where("role", "=", "member").OrderBy("id", "ASC").Limit(2).List()
	if err != nil || len(rows) != 2 {
		return fmt.Errorf("pre-UpdateBatch: %d filas err=%v", len(rows), err)
	}
	ub := []*domain.Account{&rows[0], &rows[1]}
	ub[0].Name, ub[1].Name = "badv-batch0", "badv-batch1"
	if err := quark.For[domain.Account](rec.Mark(ctx, QM("UpdateBatch")), client).UpdateBatch(ub); err != nil {
		return fmt.Errorf("UpdateBatch: %w", err)
	}
	// DeleteBatch por ids (soft) + DeleteBy por predicado.
	if n, err := quark.For[domain.Account](rec.Mark(ctx, QM("DeleteBatch")), client).DeleteBatch([]any{ub[0].ID, ub[1].ID}); err != nil || n != 2 {
		return fmt.Errorf("DeleteBatch: n=%d err=%v", n, err)
	}
	if n, err := scopedAcc(rec.Mark(ctx, QM("DeleteBy"))).Where("role", "=", "viewer").DeleteBy(); err != nil || n != 1 {
		return fmt.Errorf("DeleteBy: n=%d err=%v", n, err)
	}
	// HardDelete elimina físicamente (también lo soft-borrado), y el cleanup
	// final deja el dominio como estaba.
	all2, err := scopedAcc(ctx).WithTrashed().List()
	if err != nil {
		return fmt.Errorf("list final: %w", err)
	}
	for i := range all2 {
		if _, err := quark.For[domain.Account](rec.Mark(ctx, QM("HardDelete")), client).HardDelete(&all2[i]); err != nil {
			return fmt.Errorf("HardDelete: %w", err)
		}
	}
	if n, _ := scopedAcc(ctx).Unscoped().Count(); n != 0 {
		return fmt.Errorf("post-HardDelete: quedan %d filas badv-", n)
	}
	if _, err := quark.For[domain.Project](ctx, client).Where("name", "LIKE", "badv-%").DeleteBy(); err != nil {
		return fmt.Errorf("cleanup proyectos: %w", err)
	}
	cleanupRegistered = true
	return nil
}}

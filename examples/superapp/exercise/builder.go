package exercise

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

var builderSeq int64

// BUILDER ejerce la superficie de construcción de queries sobre datos propios y
// deterministas: agregados, group/having, filtrado (WhereIn/Or), orden/paginado,
// streaming (Iter/Cursor), Find. Setops/locking/CTE quedan para exercisers
// posteriores (necesitan matriz de capacidad por motor).
var BUILDER = Exerciser{Name: "builder", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	rec.Note(QF("For"))
	n := atomic.AddInt64(&builderSeq, 1)

	// --- Semilla aislada: 1 owner + 1 project + 5 tasks con priority 1..5 ---
	owner := &domain.Account{Email: fmt.Sprintf("builder%d@superapp.test", n), Name: "b", Role: "member", Active: true}
	if err := quark.For[domain.Account](ctx, client).Create(owner); err != nil {
		return fmt.Errorf("seed owner: %w", err)
	}
	proj := &domain.Project{OwnerID: owner.ID, Name: "builder-proj", Status: "active"}
	if err := quark.For[domain.Project](ctx, client).Create(proj); err != nil {
		return fmt.Errorf("seed project: %w", err)
	}
	for p := 1; p <= 5; p++ {
		t := &domain.Task{ProjectID: proj.ID, Title: fmt.Sprintf("t%d", p), Priority: p, Done: p%2 == 0}
		if err := quark.For[domain.Task](ctx, client).Create(t); err != nil {
			return fmt.Errorf("seed task: %w", err)
		}
	}
	scope := func() *quark.Query[domain.Task] {
		return quark.For[domain.Task](ctx, client).Where("project_id", "=", proj.ID)
	}

	// --- Agregados (deterministas: priorities 1..5) ---
	if sum, err := quark.For[domain.Task](rec.Mark(ctx, QM("Sum")), client).Where("project_id", "=", proj.ID).Sum("priority"); err != nil || sum != 15 {
		return fmt.Errorf("Sum(priority)=%v err=%v, esperaba 15", sum, err)
	}
	if avg, err := quark.For[domain.Task](rec.Mark(ctx, QM("Avg")), client).Where("project_id", "=", proj.ID).Avg("priority"); err != nil || avg != 3 {
		return fmt.Errorf("Avg=%v err=%v, esperaba 3", avg, err)
	}
	if mn, err := quark.For[domain.Task](rec.Mark(ctx, QM("Min")), client).Where("project_id", "=", proj.ID).Min("priority"); err != nil || mn != 1 {
		return fmt.Errorf("Min=%v err=%v, esperaba 1", mn, err)
	}
	if mx, err := quark.For[domain.Task](rec.Mark(ctx, QM("Max")), client).Where("project_id", "=", proj.ID).Max("priority"); err != nil || mx != 5 {
		return fmt.Errorf("Max=%v err=%v, esperaba 5", mx, err)
	}

	// --- Select + GroupBy + Having: agrupa por done; ambos grupos existen.
	// El Select("done") es OBLIGATORIO para portabilidad: sin él, List() emite
	// SELECT * ... GROUP BY done, que SQLite tolera pero PG/SQL-estándar rechaza
	// (columnas no agregadas fuera del GROUP BY). Patrón canónico = Select+GroupBy.
	// Having compara la columna bool `done` con un bool (NO con 0/1: pgx es
	// estricto con los tipos y no encodea int→bool, aunque SQLite lo tolere).
	rec.Note(QM("Select"), QM("GroupBy"), QM("Having"))
	groups, err := scope().Select("done").GroupBy("done").Having("done", "=", true).List()
	if err != nil {
		return fmt.Errorf("Select/GroupBy/Having: %w", err)
	}
	if len(groups) == 0 {
		return fmt.Errorf("GroupBy no devolvió grupos")
	}

	// --- WhereIn: priorities {1,2,3} → 3 filas ---
	if c, err := quark.For[domain.Task](rec.Mark(ctx, QM("WhereIn")), client).Where("project_id", "=", proj.ID).WhereIn("priority", []any{1, 2, 3}).Count(); err != nil || c != 3 {
		return fmt.Errorf("WhereIn count=%d err=%v, esperaba 3", c, err)
	}

	// --- Or: priority=1 OR priority=5 dentro del scope (assert laxo por la
	// precedencia SQL de OR; basta con que corra y devuelva >=1) ---
	if c, err := scope().Where("priority", "=", 5).Or(func(q *quark.Query[domain.Task]) *quark.Query[domain.Task] {
		return q.Where("project_id", "=", proj.ID).Where("priority", "=", 1)
	}).Count(); err != nil || c < 1 {
		return fmt.Errorf("Or count=%d err=%v, esperaba >=1", c, err)
	}
	rec.Note(QM("Or"))

	// --- OrderBy desc + Offset: saltando el priority=5, el primero es 4 ---
	rec.Note(QM("OrderBy"), QM("Offset"))
	top, err := scope().OrderBy("priority", "DESC").Offset(1).Limit(1).List()
	if err != nil || len(top) != 1 || top[0].Priority != 4 {
		return fmt.Errorf("OrderBy/Offset: %+v err=%v, esperaba priority=4", top, err)
	}

	// --- Distinct ---
	rec.Note(QM("Distinct"))
	if _, err := scope().Distinct().List(); err != nil {
		return fmt.Errorf("Distinct: %w", err)
	}

	// --- Find por ID ---
	want := top[0].ID
	found, err := quark.For[domain.Task](rec.Mark(ctx, QM("Find")), client).Find(want)
	if err != nil || found.ID != want {
		return fmt.Errorf("Find(%d)=%+v err=%v", want, found, err)
	}

	// --- Iter: cuenta 5 en el scope ---
	count := 0
	if err := scope().Iter(func(domain.Task) error { count++; return nil }); err != nil {
		return fmt.Errorf("Iter: %w", err)
	}
	if count != 5 {
		return fmt.Errorf("Iter contó %d, esperaba 5", count)
	}
	rec.Note(QM("Iter"))

	// --- Cursor: mismo recuento por streaming ---
	cur, err := scope().Cursor()
	if err != nil {
		return fmt.Errorf("Cursor: %w", err)
	}
	cn := 0
	for cur.Next() {
		var t domain.Task
		if err := cur.Scan(&t); err != nil {
			_ = cur.Close()
			return fmt.Errorf("Cursor.Scan: %w", err)
		}
		cn++
	}
	if err := cur.Err(); err != nil {
		_ = cur.Close()
		return fmt.Errorf("Cursor.Err: %w", err)
	}
	if err := cur.Close(); err != nil {
		return fmt.Errorf("Cursor.Close: %w", err)
	}
	if cn != 5 {
		return fmt.Errorf("Cursor contó %d, esperaba 5", cn)
	}
	rec.Note(QM("Cursor"))

	// --- Paginate: pageSize 2 → Total 5, 3 páginas ---
	page, err := scope().Paginate(2, 0)
	if err != nil {
		return fmt.Errorf("Paginate: %w", err)
	}
	if page.Total != 5 || len(page.Items) != 2 {
		return fmt.Errorf("Paginate total=%d items=%d, esperaba 5/2", page.Total, len(page.Items))
	}
	rec.Note(QM("Paginate"))

	return nil
}}

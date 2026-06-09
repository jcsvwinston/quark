package exercise

import (
	"context"
	"fmt"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// CacheExerciser ejerce las garantías de la caché L2 integrada (ADR-0004) por
// CONTEO de statements (diff de rec.Count()), no por inspección del store —
// reusa el patrón de recorder/infra_test.go. La caché la instala el suite en
// newClient (WithCacheStore(memory.New())); el store dormita para los
// exercisers que no llaman .Cache() y se cierra en el fn del suite antes del
// leak-check (cleanupLoop es una goroutine).
//
// Tres garantías observables sin tocar el store:
//
//  1. hit = 0 SQL — una 2ª query idéntica con .Cache() no ejecuta SQL (el
//     middleware del recorder no se dispara → Count() no cambia).
//  2. invalidación por mutación — un Create sobre la tabla cacheada llama
//     InvalidateTags(tabla) (la misma tag que .Cache() auto-añade), así que la
//     siguiente .Cache() vuelve a ejecutar.
//  3. N+1 acotado — un Preload de M padres suma 1 statement (hijos vía IN ...),
//     no M: el delta de Count() queda en 2 (padres + IN), no en 1+M.
var CACHE = Exerciser{Name: "cache", Fn: runCache}

func runCache(ctx context.Context, client *quark.Client, rec *recorder.Recorder, _ Conn) error {
	rec.Note(QF("For"), QM("Cache"), QM("Where"), QM("List"), QM("Preload"), QM("Create"))

	// Semilla aislada (prefijo único, no choca con crud/relations).
	owner := &domain.Account{Email: "cache-owner@superapp.test", Name: "cache-owner", Role: "member", Active: true}
	if err := quark.For[domain.Account](rec.Mark(ctx, QM("Create")), client).Create(owner); err != nil {
		return fmt.Errorf("seed owner: %w", err)
	}

	// cachedQuery: misma SQL+args en cada llamada → misma cache key. Auto-tag =
	// nombre de tabla de Account (la que invalida el Create de más abajo).
	cachedQuery := func() ([]domain.Account, error) {
		return quark.For[domain.Account](rec.Mark(ctx, QM("List")), client).
			Where("email", "=", owner.Email).
			Cache(time.Minute).
			List()
	}

	// --- 1) hit = 0 SQL ---
	before := rec.Count()
	if _, err := cachedQuery(); err != nil {
		return fmt.Errorf("cache miss query: %w", err)
	}
	afterMiss := rec.Count()
	if afterMiss <= before {
		return fmt.Errorf("cache: la 1ª query (miss) no ejecutó SQL (Count %d→%d)", before, afterMiss)
	}
	if _, err := cachedQuery(); err != nil {
		return fmt.Errorf("cache hit query: %w", err)
	}
	if afterHit := rec.Count(); afterHit != afterMiss {
		return fmt.Errorf("cache: 2ª query idéntica debió ser HIT (0 SQL), pero Count %d→%d", afterMiss, afterHit)
	}

	// --- 2) invalidación por mutación ---
	// Mutar la tabla accounts → InvalidateTags(accounts) borra la entrada
	// cacheada (tagueada con esa misma tabla), así que la siguiente cachedQuery
	// vuelve a pegar a la BD.
	bump := &domain.Account{Email: "cache-bump@superapp.test", Name: "cache-bump", Role: "member", Active: true}
	if err := quark.For[domain.Account](rec.Mark(ctx, QM("Create")), client).Create(bump); err != nil {
		return fmt.Errorf("seed bump (invalidación): %w", err)
	}
	beforeReq := rec.Count()
	if _, err := cachedQuery(); err != nil {
		return fmt.Errorf("cache requery tras invalidación: %w", err)
	}
	if rec.Count() <= beforeReq {
		return fmt.Errorf("cache: tras mutar accounts la query cacheada debió invalidarse y re-ejecutar, pero Count no cambió (%d)", beforeReq)
	}

	// --- 3) N+1 acotado: M padres con Preload → 2 statements, no 1+M ---
	// Las siembras van con `ctx` desnudo (sin rec.Mark) a propósito: ocurren
	// ANTES de `beforePreload`, fuera de la ventana de medición del delta, así
	// que no contaminan el conteo aunque queden en rec.Count() acumulado.
	const parents, kids = 3, 2
	for i := 0; i < parents; i++ {
		o := &domain.Account{Email: fmt.Sprintf("cache-n1-%d@superapp.test", i), Name: "cache-n1", Role: "member", Active: true}
		if err := quark.For[domain.Account](ctx, client).Create(o); err != nil {
			return fmt.Errorf("seed n1 owner %d: %w", i, err)
		}
		for j := 0; j < kids; j++ {
			p := &domain.Project{OwnerID: o.ID, Name: "cache-n1-proj", Status: "active"}
			if err := quark.For[domain.Project](ctx, client).Create(p); err != nil {
				return fmt.Errorf("seed n1 project %d/%d: %w", i, j, err)
			}
		}
	}
	beforePreload := rec.Count()
	accs, err := quark.For[domain.Account](rec.Mark(ctx, QM("List")), client).
		Preload("Projects").Where("name", "=", "cache-n1").List()
	if err != nil {
		return fmt.Errorf("preload list (N+1): %w", err)
	}
	delta := rec.Count() - beforePreload
	if len(accs) != parents {
		return fmt.Errorf("N+1: esperaba %d cuentas, obtuve %d", parents, len(accs))
	}
	loaded := 0
	for _, a := range accs {
		loaded += len(a.Projects)
	}
	if loaded != parents*kids {
		return fmt.Errorf("N+1: el Preload trajo %d proyectos, esperaba %d", loaded, parents*kids)
	}
	// 2 esperado (padres + 1 IN para todos los hijos). Umbral en >3 (no ==2)
	// con +1 de holgura por si algún motor emite un statement de sesión extra
	// (p.ej. un SET); >3 ya delata el patrón 1-query-por-padre (1+M = 4 con M=3).
	if delta > 3 {
		return fmt.Errorf("N+1: Preload de %d padres disparó %d statements (esperaba 2: padres + IN hijos)", parents, delta)
	}
	return nil
}

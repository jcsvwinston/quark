// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package exercise

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/acceptance/domain"
	"github.com/jcsvwinston/quark/acceptance/recorder"
)

// LIKEESCAPE ejerce las superficies escapadas de LIKE (QK-25, A8 S2) en los
// seis motores: el builder (WhereLike/WhereNotLike/WhereContains/
// WhereStartsWith/WhereEndsWith), la columna tipada (Contains/StartsWith/
// EndsWith/LikeEscaped/NotLikeEscaped), el AST (Like/NotLike/Contains/
// StartsWith/EndsWith) y EscapeLike. La fixture lleva UNA fila con un `%`
// literal, UNA con `_` y UNA con un `[`: cada búsqueda del carácter que el
// usuario teclea debe devolver exactamente esa fila — 3 es «el comodín siguió
// siendo comodín» y 0 es «el valor se escapó sin declarar el carácter» (la
// forma que rompe SQLite). El corchete es comodín sólo en SQL Server y Oracle
// rechaza escaparlo (ORA-01424), así que su búsqueda es la que prueba que el
// patrón se compone POR MOTOR.
//
// Las filas-sonda llevan el marcador "lesc-" y se eliminan al salir.
var LIKEESCAPE = Exerciser{Name: "like-escape", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, _ Conn) error {
	scoped := func(c context.Context) *quark.Query[domain.Account] {
		return quark.For[domain.Account](c, client).Where("email", "LIKE", "lesc-%")
	}
	names := []string{"lesc 100% off", "lesc under_score", "lesc [bracket]", "lesc plain"}
	accs := make([]*domain.Account, len(names))
	for i, n := range names {
		accs[i] = &domain.Account{Email: fmt.Sprintf("lesc-%02d@superapp.test", i), Name: n, Role: "member", Active: true}
	}
	if err := quark.For[domain.Account](ctx, client).CreateBatch(accs); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	defer func() {
		cctx := context.Background()
		rows, _ := quark.For[domain.Account](cctx, client).Where("email", "LIKE", "lesc-%").WithTrashed().List()
		for i := range rows {
			_, _ = quark.For[domain.Account](cctx, client).HardDelete(&rows[i])
		}
	}()

	// one asserta que la consulta devuelve exactamente la fila esperada.
	one := func(label string, q *quark.Query[domain.Account], want string) error {
		rows, err := q.List()
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if len(rows) != 1 || rows[0].Name != want {
			got := make([]string, len(rows))
			for i := range rows {
				got[i] = rows[i].Name
			}
			return fmt.Errorf("%s: filas %v, esperaba exactamente %q (3 = el comodín siguió siendo comodín; 0 = valor escapado sin declarar el carácter)", label, got, want)
		}
		return nil
	}

	// --- builder ---
	if err := one("WhereContains(%)", scoped(rec.Mark(ctx, QM("WhereContains"))).WhereContains("name", "%"), "lesc 100% off"); err != nil {
		return err
	}
	if err := one("WhereContains([)", scoped(ctx).WhereContains("name", "["), "lesc [bracket]"); err != nil {
		return err
	}
	if err := one("WhereStartsWith", scoped(rec.Mark(ctx, QM("WhereStartsWith"))).WhereStartsWith("name", "lesc under_"), "lesc under_score"); err != nil {
		return err
	}
	if err := one("WhereEndsWith", scoped(rec.Mark(ctx, QM("WhereEndsWith"))).WhereEndsWith("name", "% off"), "lesc 100% off"); err != nil {
		return err
	}
	rec.Note(QF("EscapeLike"))
	if err := one("WhereLike+EscapeLike", scoped(rec.Mark(ctx, QM("WhereLike"))).WhereLike("name", "%"+quark.EscapeLike("100%")+"%"), "lesc 100% off"); err != nil {
		return err
	}
	if n, err := scoped(rec.Mark(ctx, QM("WhereNotLike"))).WhereNotLike("name", `%\%%`).Count(); err != nil || n != 3 {
		return fmt.Errorf("WhereNotLike: n=%d err=%v, esperaba 3", n, err)
	}
	// El guard lee el valor: un escape colgante se rechaza antes del motor.
	if _, err := scoped(ctx).WhereLike("name", `abc\`).List(); !errors.Is(err, quark.ErrInvalidQuery) {
		return fmt.Errorf("WhereLike con escape colgante: err=%v, esperaba ErrInvalidQuery", err)
	}

	// --- columna tipada ---
	rec.Note(QF("NewTypedStringColumn"),
		QF("(TypedStringColumn).Contains"), QF("(TypedStringColumn).StartsWith"), QF("(TypedStringColumn).EndsWith"),
		QF("(TypedStringColumn).LikeEscaped"), QF("(TypedStringColumn).NotLikeEscaped"))
	name := quark.NewTypedStringColumn("name")
	if err := one("typed Contains", scoped(ctx).WhereP(name.Contains("_")), "lesc under_score"); err != nil {
		return err
	}
	if err := one("typed StartsWith", scoped(ctx).WhereP(name.StartsWith("lesc [")), "lesc [bracket]"); err != nil {
		return err
	}
	if err := one("typed EndsWith", scoped(ctx).WhereP(name.EndsWith("_score")), "lesc under_score"); err != nil {
		return err
	}
	if err := one("typed LikeEscaped", scoped(ctx).WhereP(name.LikeEscaped(`%\%%`)), "lesc 100% off"); err != nil {
		return err
	}
	if n, err := scoped(ctx).WhereP(name.NotLikeEscaped(`%\%%`)).Count(); err != nil || n != 3 {
		return fmt.Errorf("typed NotLikeEscaped: n=%d err=%v, esperaba 3", n, err)
	}

	// --- AST ---
	rec.Note(QF("Like"), QF("NotLike"), QF("Contains"), QF("StartsWith"), QF("EndsWith"))
	if err := one("AST Contains", scoped(ctx).WhereExpr(quark.Contains(quark.Col("name"), "%")), "lesc 100% off"); err != nil {
		return err
	}
	if err := one("AST StartsWith", scoped(ctx).WhereExpr(quark.StartsWith(quark.Col("name"), "lesc under_")), "lesc under_score"); err != nil {
		return err
	}
	if err := one("AST EndsWith", scoped(ctx).WhereExpr(quark.EndsWith(quark.Col("name"), "bracket]")), "lesc [bracket]"); err != nil {
		return err
	}
	if err := one("AST Like", scoped(ctx).WhereExpr(quark.Like(quark.Col("name"), `%\%%`)), "lesc 100% off"); err != nil {
		return err
	}
	if n, err := scoped(ctx).WhereExpr(quark.NotLike(quark.Col("name"), `%\%%`)).Count(); err != nil || n != 3 {
		return fmt.Errorf("AST NotLike: n=%d err=%v, esperaba 3", n, err)
	}

	// Y la forma plana sigue siendo la publicada: el patrón opaco ensancha.
	if n, err := scoped(ctx).Where("name", "LIKE", "%%%").Count(); err != nil || n != 4 {
		return fmt.Errorf("Where LIKE plano: n=%d err=%v, esperaba 4 (la forma plana no cambia: QK-32)", n, err)
	}
	return nil
}}

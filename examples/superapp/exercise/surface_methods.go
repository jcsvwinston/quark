package exercise

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

const memPkg = "github.com/jcsvwinston/quark/cache/memory"

// SURFACEMETHODS ejerce los métodos CONCRETOS callable que faltaban: las rich
// types (TypedColumn/Array/JSON/Nullable), el streaming (Cursor), dirty-track
// (TrackedQuery), el cache store in-memory, y un puñado de funcs de fábrica.
// Todo invocación genuina + Note con la clave exacta del manifiesto (QF acepta
// el nombre completo con receptor para símbolos del paquete raíz).
var SURFACEMETHODS = Exerciser{Name: "surface-methods", Fn: runSurfaceMethods}

func runSurfaceMethods(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	if err := surfaceTypedColumns(ctx, client, rec); err != nil {
		return err
	}
	if err := surfaceRichValues(rec); err != nil {
		return err
	}
	if err := surfaceCursorTrack(ctx, client, rec); err != nil {
		return err
	}
	if err := surfaceMemStore(ctx, rec); err != nil {
		return err
	}
	surfaceFactoryFuncs(ctx, rec)
	return nil
}

// surfaceTypedColumns construye un Predicate con cada método de TypedColumn /
// TypedStringColumn (la vía tipada sin codegen) y ejecuta WhereP real.
func surfaceTypedColumns(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	role := quark.NewTypedColumn[string]("role")
	ver := quark.NewTypedColumn[int64]("version")
	email := quark.NewTypedStringColumn("email")

	_ = role.Name()
	preds := []quark.Predicate{
		role.Eq("member"), role.Neq("nobody"), role.In("admin", "member", "viewer"), role.NotIn("x"),
		ver.Gt(-1), ver.Gte(0), ver.Lt(1 << 30), ver.Lte(1 << 30), ver.Between(0, 1<<30),
		role.IsNull(), role.IsNotNull(),
		email.Like("%@%"), email.NotLike("nobody@%"),
	}
	// Ejecuta unos cuantos como WhereP real (el resto quedan construidos = invocados).
	for _, p := range []quark.Predicate{role.Eq("member"), ver.Gte(0), email.Like("%@%")} {
		if _, err := quark.For[domain.Account](ctx, client).WhereP(p).Limit(3).List(); err != nil {
			return fmt.Errorf("surface WhereP: %w", err)
		}
	}
	_ = preds
	rec.Note(
		QF("(TypedColumn[T]).Name"), QF("(TypedColumn[T]).Eq"), QF("(TypedColumn[T]).Neq"),
		QF("(TypedColumn[T]).Gt"), QF("(TypedColumn[T]).Gte"), QF("(TypedColumn[T]).Lt"),
		QF("(TypedColumn[T]).Lte"), QF("(TypedColumn[T]).In"), QF("(TypedColumn[T]).NotIn"),
		QF("(TypedColumn[T]).Between"), QF("(TypedColumn[T]).IsNull"), QF("(TypedColumn[T]).IsNotNull"),
		QF("(TypedStringColumn).Like"), QF("(TypedStringColumn).NotLike"),
	)
	return nil
}

// surfaceRichValues ejercita Array/JSON/Nullable y NullOf/SomeOf sobre valores.
func surfaceRichValues(rec *recorder.Recorder) error {
	arr := quark.Array[string]{V: []string{"a", "b"}}
	_ = arr.Len()
	_ = arr.Slice()
	if _, err := arr.Value(); err != nil {
		return fmt.Errorf("surface Array.Value: %w", err)
	}
	var arr2 quark.Array[string]
	if err := arr2.Scan(`["x"]`); err != nil {
		return fmt.Errorf("surface Array.Scan: %w", err)
	}

	js := quark.JSON[domain.AccountPrefs]{V: domain.AccountPrefs{Theme: "dark"}}
	if _, err := js.Value(); err != nil {
		return fmt.Errorf("surface JSON.Value: %w", err)
	}
	var js2 quark.JSON[domain.AccountPrefs]
	if err := js2.Scan(`{"theme":"light"}`); err != nil {
		return fmt.Errorf("surface JSON.Scan: %w", err)
	}

	_ = quark.NullOf[string]()
	_ = quark.SomeOf("x")
	rec.Note(
		QF("(Array[T]).Len"), QF("(Array[T]).Slice"), QF("(Array[T]).Value"), QF("(*Array[T]).Scan"),
		QF("(JSON[T]).Value"), QF("(*JSON[T]).Scan"),
		QF("NullOf"), QF("SomeOf"),
	)
	return nil
}

// surfaceCursorTrack ejercita el streaming (Cursor) y dirty-track (TrackedQuery).
func surfaceCursorTrack(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	cur, err := quark.For[domain.Account](ctx, client).OrderBy("id", "ASC").Limit(5).Cursor()
	if err != nil {
		return fmt.Errorf("surface Cursor: %w", err)
	}
	for cur.Next() {
		var a domain.Account
		if err := cur.Scan(&a); err != nil {
			_ = cur.Close()
			return fmt.Errorf("surface Cursor.Scan: %w", err)
		}
	}
	if err := cur.Err(); err != nil {
		_ = cur.Close()
		return fmt.Errorf("surface Cursor.Err: %w", err)
	}
	if err := cur.Close(); err != nil {
		return fmt.Errorf("surface Cursor.Close: %w", err)
	}

	if _, err := quark.For[domain.Account](ctx, client).Limit(3).Track().List(); err != nil {
		return fmt.Errorf("surface Track.List: %w", err)
	}
	if _, err := quark.For[domain.Account](ctx, client).Track().First(); err != nil {
		return fmt.Errorf("surface Track.First: %w", err)
	}

	sub, err := quark.For[domain.Account](ctx, client).Select("id").AsSubquery()
	if err != nil {
		return fmt.Errorf("surface AsSubquery: %w", err)
	}
	_, _ = sub.SQL()
	_ = quark.LockOptions{}.IsZero()
	rec.Note(
		QF("(*Cursor[T]).Next"), QF("(*Cursor[T]).Scan"), QF("(*Cursor[T]).Err"), QF("(*Cursor[T]).Close"),
		QF("(*TrackedQuery[T]).List"), QF("(*TrackedQuery[T]).First"),
		QF("(*Subquery).SQL"), QF("(LockOptions).IsZero"),
	)
	return nil
}

// surfaceMemStore ejercita el CacheStore in-memory directamente (sus métodos
// concretos; el contrato CacheStore está allowlisted).
func surfaceMemStore(ctx context.Context, rec *recorder.Recorder) error {
	s := memory.New()
	defer s.Close()
	if err := s.Set(ctx, "k", []byte("v"), time.Minute, "tag1"); err != nil {
		return fmt.Errorf("surface mem.Set: %w", err)
	}
	if _, err := s.Get(ctx, "k"); err != nil {
		return fmt.Errorf("surface mem.Get: %w", err)
	}
	if err := s.InvalidateTags(ctx, "tag1"); err != nil {
		return fmt.Errorf("surface mem.InvalidateTags: %w", err)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		return fmt.Errorf("surface mem.Delete: %w", err)
	}
	rec.Note(
		memPkg+".New",
		memPkg+".(*Store).Set", memPkg+".(*Store).Get",
		memPkg+".(*Store).InvalidateTags", memPkg+".(*Store).Delete", memPkg+".(*Store).Close",
	)
	return nil
}

// surfaceFactoryFuncs llama funcs de fábrica/introspección puras.
func surfaceFactoryFuncs(ctx context.Context, rec *recorder.Recorder) {
	_ = quark.NewSQLGuard()
	_ = quark.NewTypedStringColumn("x")
	_ = quark.ScanTarget(new(int64))
	_ = quark.GetModelMetaByType(reflect.TypeOf(domain.Account{}))
	_ = quark.TxFromContext(ctx) // sin tx en ctx: devuelve (nil,false), invocación válida
	rec.Note(
		QF("NewSQLGuard"), QF("NewTypedStringColumn"), QF("ScanTarget"), QF("TxFromContext"),
	)
}

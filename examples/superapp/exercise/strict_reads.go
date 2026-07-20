package exercise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// srProbe es la sonda del modo estricto de lecturas (#247). Tabla propia para
// no interferir con los conteos de los demás exercisers.
type srProbe struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (srProbe) TableName() string { return "sr_probes" }

// STRICTREADS ejerce WithStrictReads / TrackReads / AllowUnbounded (#247) con
// asserts funcionales sobre el log, no solo invocación:
//
//   - Warn: Iter() sin Limit() emite el WARN estructurado; AllowUnbounded()
//     lo silencia (escape hatch para exports); Limit() también.
//   - Reject: Iter()/Cursor() sin límite devuelven ErrInvalidQuery.
//   - N+1: el bucle clásico de Find por PK bajo un ctx de TrackReads dispara
//     UN WARN por ctx+tabla; sin TrackReads no se cuenta nada.
//
// Corre en los 6 motores: no necesita aprovisionar nada.
var STRICTREADS = Exerciser{Name: "strictreads", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	rec.Note(QF("WithStrictReads"), QF("TrackReads"), QM("AllowUnbounded"),
		QF("StrictReadsMode"), QF("StrictReadsOff"), QF("StrictReadsWarn"), QF("StrictReadsReject"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	l := quark.DefaultLimits()
	l.SafeMigrations = false

	warnClient, err := quark.New(conn.Driver, conn.DSN, append(rec.Options(),
		quark.WithLogger(logger),
		quark.WithStrictReads(quark.StrictReadsWarn),
		quark.WithLimits(l))...)
	if err != nil {
		return fmt.Errorf("client strict-warn: %w", err)
	}
	defer warnClient.Close()

	if err := warnClient.Migrate(ctx, &srProbe{}); err != nil {
		return fmt.Errorf("migrate sr_probes: %w", err)
	}
	defer func() { _, _ = warnClient.Raw().ExecContext(context.Background(), "DROP TABLE sr_probes") }()
	_, _ = warnClient.Raw().ExecContext(ctx, "DELETE FROM sr_probes")

	probe := &srProbe{Name: "strict"}
	if err := quark.For[srProbe](ctx, warnClient).Create(probe); err != nil {
		return fmt.Errorf("create sonda: %w", err)
	}

	// --- Warn: Iter sin límite debe avisar; con escape hatch, no. ------------
	logBuf.Reset()
	if err := quark.For[srProbe](rec.Mark(ctx, QM("Iter")), warnClient).Iter(func(srProbe) error { return nil }); err != nil {
		return fmt.Errorf("iter unbounded (warn): %w", err)
	}
	if !strings.Contains(logBuf.String(), "unbounded read under strict reads") {
		return fmt.Errorf("Iter sin Limit bajo StrictReadsWarn no emitió el WARN; log: %q", logBuf.String())
	}
	logBuf.Reset()
	if err := quark.For[srProbe](ctx, warnClient).AllowUnbounded().Iter(func(srProbe) error { return nil }); err != nil {
		return fmt.Errorf("iter AllowUnbounded: %w", err)
	}
	if err := quark.For[srProbe](ctx, warnClient).Limit(1).Iter(func(srProbe) error { return nil }); err != nil {
		return fmt.Errorf("iter con Limit: %w", err)
	}
	if strings.Contains(logBuf.String(), "unbounded read under strict reads") {
		return fmt.Errorf("AllowUnbounded()/Limit() no silenciaron el WARN; log: %q", logBuf.String())
	}

	// --- N+1: bucle clásico de Find por PK bajo TrackReads → UN WARN. --------
	logBuf.Reset()
	tracked := quark.TrackReads(ctx)
	for i := 0; i < 12; i++ {
		if _, err := quark.For[srProbe](tracked, warnClient).Find(probe.ID); err != nil {
			return fmt.Errorf("find n+1 (%d): %w", i, err)
		}
	}
	if got := strings.Count(logBuf.String(), "possible N+1 read pattern"); got != 1 {
		return fmt.Errorf("esperaba exactamente 1 WARN de N+1, hubo %d; log: %q", got, logBuf.String())
	}
	logBuf.Reset()
	for i := 0; i < 12; i++ {
		if _, err := quark.For[srProbe](ctx, warnClient).Find(probe.ID); err != nil {
			return fmt.Errorf("find sin tracking (%d): %w", i, err)
		}
	}
	if strings.Contains(logBuf.String(), "possible N+1 read pattern") {
		return fmt.Errorf("el detector N+1 contó lecturas sin TrackReads; log: %q", logBuf.String())
	}

	// --- Reject: sin límite ni escape hatch → ErrInvalidQuery. ---------------
	rejectClient, err := quark.New(conn.Driver, conn.DSN, append(rec.Options(),
		quark.WithLogger(logger),
		quark.WithStrictReads(quark.StrictReadsReject),
		quark.WithLimits(l))...)
	if err != nil {
		return fmt.Errorf("client strict-reject: %w", err)
	}
	defer rejectClient.Close()

	if err := quark.For[srProbe](ctx, rejectClient).Iter(func(srProbe) error { return nil }); !errors.Is(err, quark.ErrInvalidQuery) {
		return fmt.Errorf("Iter unbounded bajo StrictReadsReject: err=%v, esperaba ErrInvalidQuery", err)
	}
	if _, err := quark.For[srProbe](rec.Mark(ctx, QM("Cursor")), rejectClient).Cursor(); !errors.Is(err, quark.ErrInvalidQuery) {
		return fmt.Errorf("Cursor unbounded bajo StrictReadsReject: err=%v, esperaba ErrInvalidQuery", err)
	}
	if cur, err := quark.For[srProbe](ctx, rejectClient).AllowUnbounded().Cursor(); err != nil {
		return fmt.Errorf("Cursor AllowUnbounded bajo reject: %w", err)
	} else {
		_ = cur.Close()
	}
	return nil
}}

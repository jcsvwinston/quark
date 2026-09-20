// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package exercise

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/acceptance/recorder"
)

// NATIVETYPES ejerce los tipos de A8 S6 en los seis motores: un slice crudo,
// un map, quark.Range[T] y net.IP escritos y leídos por el tipo de columna que
// Migrate les da (array/rango/inet nativos en PostgreSQL, JSON o texto en el
// resto), y los tres métodos públicos de Range: Value, Scan y PGLiteral.
var NATIVETYPES = Exerciser{Name: "native-types", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, _ Conn) error {
	type ntEvent struct {
		ID     int64                  `db:"id" pk:"true"`
		Tags   []string               `db:"tags"`
		Window quark.Range[time.Time] `db:"window"`
		Source net.IP                 `db:"source"`
	}
	if err := client.Migrate(ctx, &ntEvent{}); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer func() { _, _ = client.Raw().ExecContext(context.Background(), "DROP TABLE nt_events") }()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	rec.Note(QF("(Range[T]).Value"), QF("(*Range[T]).Scan"), QF("(Range[T]).PGLiteral"))
	row := &ntEvent{Tags: []string{"go", "orm"}, Window: quark.Range[time.Time]{Lower: now, Upper: now.Add(time.Hour)}, Source: net.ParseIP("10.0.0.1")}
	if _, err := row.Window.Value(); err != nil {
		return fmt.Errorf("Range.Value: %w", err)
	}
	if _, err := row.Window.PGLiteral(); err != nil {
		return fmt.Errorf("Range.PGLiteral: %w", err)
	}
	if err := quark.For[ntEvent](ctx, client).Create(row); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	got, err := quark.For[ntEvent](ctx, client).Find(row.ID)
	if err != nil {
		return fmt.Errorf("find: %w", err)
	}
	if len(got.Tags) != 2 || !got.Window.Lower.Equal(now) || !got.Source.Equal(row.Source) {
		return fmt.Errorf("round trip: %+v", got)
	}
	var back quark.Range[time.Time]
	if err := back.Scan("[2026-01-01 00:00:00+00,2026-01-02 00:00:00+00)"); err != nil {
		return fmt.Errorf("Range.Scan: %w", err)
	}
	return nil
}}

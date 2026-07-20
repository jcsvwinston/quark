// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

const (
	strictWarnMsg  = "unbounded read under strict reads"
	nPlusOneMsg    = "possible N+1 read pattern"
	strictTestConn = "file:strictreads?mode=memory&cache=shared"
)

type StrictCustomer struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type StrictOrder struct {
	ID         int64           `db:"id" pk:"true"`
	Ref        string          `db:"ref"`
	CustomerID int64           `db:"customer_id"`
	Customer   *StrictCustomer `rel:"belongs_to" join:"customer_id"`
}

// newStrictClient builds a shared-memory SQLite client with the given
// strict-reads mode and a WARN-capturing logger.
func newStrictClient(t *testing.T, mode quark.StrictReadsMode, buf *bytes.Buffer) *quark.Client {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client, err := quark.New("sqlite", strictTestConn,
		quark.WithLogger(logger),
		quark.WithStrictReads(mode),
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()
	if err := client.Migrate(ctx, &StrictCustomer{}, &StrictOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return client
}

func seedStrictData(t *testing.T, client *quark.Client, n int) []StrictOrder {
	t.Helper()
	ctx := context.Background()
	orders := make([]StrictOrder, 0, n)
	for i := 0; i < n; i++ {
		c := &StrictCustomer{Name: fmt.Sprintf("customer-%d", i)}
		if err := quark.For[StrictCustomer](ctx, client).Create(c); err != nil {
			t.Fatalf("create customer %d: %v", i, err)
		}
		o := &StrictOrder{Ref: fmt.Sprintf("order-%d", i), CustomerID: c.ID}
		if err := quark.For[StrictOrder](ctx, client).Create(o); err != nil {
			t.Fatalf("create order %d: %v", i, err)
		}
		orders = append(orders, *o)
	}
	return orders
}

// TestStrictReadsWarn covers the WARN half of the escalation: an unbounded
// Iter()/Cursor() logs a structured WARN; an explicit Limit() or the
// AllowUnbounded() escape hatch silences it.
func TestStrictReadsWarn(t *testing.T) {
	var buf bytes.Buffer
	client := newStrictClient(t, quark.StrictReadsWarn, &buf)
	seedStrictData(t, client, 3)
	ctx := context.Background()

	t.Run("Iter without Limit warns", func(t *testing.T) {
		buf.Reset()
		if err := quark.For[StrictOrder](ctx, client).Iter(func(StrictOrder) error { return nil }); err != nil {
			t.Fatalf("Iter: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, strictWarnMsg) {
			t.Errorf("unbounded Iter did not WARN.\nlog: %q", out)
		}
		if !strings.Contains(out, "strict_orders") || !strings.Contains(out, "Iter()") {
			t.Errorf("WARN should carry table and entrypoint attributes.\nlog: %q", out)
		}
	})

	t.Run("Cursor without Limit warns", func(t *testing.T) {
		buf.Reset()
		cur, err := quark.For[StrictOrder](ctx, client).Cursor()
		if err != nil {
			t.Fatalf("Cursor: %v", err)
		}
		cur.Close()
		out := buf.String()
		if !strings.Contains(out, strictWarnMsg) || !strings.Contains(out, "Cursor()") {
			t.Errorf("unbounded Cursor did not WARN with its entrypoint.\nlog: %q", out)
		}
	})

	t.Run("explicit Limit silences", func(t *testing.T) {
		buf.Reset()
		if err := quark.For[StrictOrder](ctx, client).Limit(2).Iter(func(StrictOrder) error { return nil }); err != nil {
			t.Fatalf("Iter: %v", err)
		}
		if strings.Contains(buf.String(), strictWarnMsg) {
			t.Errorf("bounded Iter must not WARN.\nlog: %q", buf.String())
		}
	})

	t.Run("AllowUnbounded silences", func(t *testing.T) {
		buf.Reset()
		if err := quark.For[StrictOrder](ctx, client).AllowUnbounded().Iter(func(StrictOrder) error { return nil }); err != nil {
			t.Fatalf("Iter: %v", err)
		}
		if strings.Contains(buf.String(), strictWarnMsg) {
			t.Errorf("AllowUnbounded Iter must not WARN.\nlog: %q", buf.String())
		}
	})
}

// TestStrictReadsOff pins the default: without WithStrictReads an unbounded
// Iter()/Cursor() stays silent — the historical behaviour is untouched.
func TestStrictReadsOff(t *testing.T) {
	var buf bytes.Buffer
	client := newStrictClient(t, quark.StrictReadsOff, &buf)
	seedStrictData(t, client, 2)
	ctx := context.Background()

	buf.Reset()
	if err := quark.For[StrictOrder](ctx, client).Iter(func(StrictOrder) error { return nil }); err != nil {
		t.Fatalf("Iter: %v", err)
	}
	cur, err := quark.For[StrictOrder](ctx, client).Cursor()
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	cur.Close()
	if strings.Contains(buf.String(), strictWarnMsg) || strings.Contains(buf.String(), nPlusOneMsg) {
		t.Errorf("strict-reads output leaked with the mode off.\nlog: %q", buf.String())
	}
}

// TestStrictReadsReject covers the reject half: an unbounded Iter()/Cursor()
// returns ErrInvalidQuery; Limit() and AllowUnbounded() both unlock it.
func TestStrictReadsReject(t *testing.T) {
	var buf bytes.Buffer
	client := newStrictClient(t, quark.StrictReadsReject, &buf)
	seedStrictData(t, client, 2)
	ctx := context.Background()

	err := quark.For[StrictOrder](ctx, client).Iter(func(StrictOrder) error { return nil })
	if !errors.Is(err, quark.ErrInvalidQuery) {
		t.Errorf("unbounded Iter under reject: err = %v, want ErrInvalidQuery", err)
	}

	_, err = quark.For[StrictOrder](ctx, client).Cursor()
	if !errors.Is(err, quark.ErrInvalidQuery) {
		t.Errorf("unbounded Cursor under reject: err = %v, want ErrInvalidQuery", err)
	}

	if err := quark.For[StrictOrder](ctx, client).Limit(1).Iter(func(StrictOrder) error { return nil }); err != nil {
		t.Errorf("bounded Iter under reject should run: %v", err)
	}

	if err := quark.For[StrictOrder](ctx, client).AllowUnbounded().Iter(func(StrictOrder) error { return nil }); err != nil {
		t.Errorf("AllowUnbounded Iter under reject should run: %v", err)
	}
	cur, err := quark.For[StrictOrder](ctx, client).AllowUnbounded().Cursor()
	if err != nil {
		t.Errorf("AllowUnbounded Cursor under reject should run: %v", err)
	} else {
		cur.Close()
	}
}

// TestStrictReadsNPlusOne covers the detector end to end: the classic loop
// of Find-by-PK fires exactly ONE WARN per context+table at the threshold,
// an untracked context counts nothing, and the Preload alternative loads
// the same data without tripping the detector.
func TestStrictReadsNPlusOne(t *testing.T) {
	var buf bytes.Buffer
	client := newStrictClient(t, quark.StrictReadsWarn, &buf)
	orders := seedStrictData(t, client, 12)

	t.Run("classic loop fires once at the threshold", func(t *testing.T) {
		buf.Reset()
		tracked := quark.TrackReads(context.Background())

		// Nine point reads: below the threshold, silence.
		for _, o := range orders[:9] {
			if _, err := quark.For[StrictCustomer](tracked, client).Find(o.CustomerID); err != nil {
				t.Fatalf("Find: %v", err)
			}
		}
		if strings.Contains(buf.String(), nPlusOneMsg) {
			t.Fatalf("N+1 WARN fired below the threshold.\nlog: %q", buf.String())
		}

		// The 10th read crosses it; the remaining reads must not re-fire.
		for _, o := range orders[9:] {
			if _, err := quark.For[StrictCustomer](tracked, client).Find(o.CustomerID); err != nil {
				t.Fatalf("Find: %v", err)
			}
		}
		out := buf.String()
		if got := strings.Count(out, nPlusOneMsg); got != 1 {
			t.Errorf("N+1 WARN count = %d, want exactly 1.\nlog: %q", got, out)
		}
		if !strings.Contains(out, "strict_customers") {
			t.Errorf("N+1 WARN should name the table.\nlog: %q", out)
		}
	})

	t.Run("untracked context counts nothing", func(t *testing.T) {
		buf.Reset()
		ctx := context.Background()
		for _, o := range orders {
			if _, err := quark.For[StrictCustomer](ctx, client).Find(o.CustomerID); err != nil {
				t.Fatalf("Find: %v", err)
			}
		}
		if strings.Contains(buf.String(), nPlusOneMsg) {
			t.Errorf("N+1 WARN fired without TrackReads.\nlog: %q", buf.String())
		}
	})

	t.Run("Preload loads the relation without tripping the detector", func(t *testing.T) {
		buf.Reset()
		tracked := quark.TrackReads(context.Background())
		got, err := quark.For[StrictOrder](tracked, client).Preload("Customer").Limit(len(orders)).List()
		if err != nil {
			t.Fatalf("Preload List: %v", err)
		}
		if len(got) != len(orders) {
			t.Fatalf("Preload List returned %d rows, want %d", len(got), len(orders))
		}
		for _, o := range got {
			if o.Customer == nil {
				t.Fatalf("order %d: Customer not preloaded", o.ID)
			}
		}
		if strings.Contains(buf.String(), nPlusOneMsg) {
			t.Errorf("Preload path tripped the N+1 detector.\nlog: %q", buf.String())
		}
	})
}

// BenchmarkStrictReadsPointRead measures the point-read hot path with the
// feature off (the default every existing caller runs) and with WARN mode
// on over an untracked context. The off path adds one integer comparison
// and zero allocations relative to v1.3.3; run with -benchmem to compare.
func BenchmarkStrictReadsPointRead(b *testing.B) {
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		mode quark.StrictReadsMode
	}{
		{"off", quark.StrictReadsOff},
		{"warn-untracked", quark.StrictReadsWarn},
	} {
		b.Run(tc.name, func(b *testing.B) {
			client, err := quark.New("sqlite", fmt.Sprintf("file:strictbench_%s?mode=memory&cache=shared", tc.name),
				quark.WithLogger(quiet),
				quark.WithStrictReads(tc.mode),
			)
			if err != nil {
				b.Fatal(err)
			}
			defer client.Close()
			if err := client.Migrate(ctx, &StrictCustomer{}); err != nil {
				b.Fatal(err)
			}
			c := &StrictCustomer{Name: "bench"}
			if err := quark.For[StrictCustomer](ctx, client).Create(c); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := quark.For[StrictCustomer](ctx, client).Find(c.ID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

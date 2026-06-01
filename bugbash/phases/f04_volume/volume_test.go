// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f04_volume is bug-bash phase F4: behaviour under a realistic-volume
// dataset, per engine.
//
//   - List() without an explicit Limit() applies the documented safety cap of
//     100 rows (it truncates, it does not OOM); an explicit Limit() overrides it.
//   - Deep Offset pagination walks the full set with no gaps and no duplicates.
//   - Cursor() streams the full set server-side and closes cleanly.
//   - Iter() stops early when the callback returns an error, and (best-effort,
//     driver-dependent) observes context cancellation mid-stream.
//   - Paginate() reports an exact Total and a correct partial last page.
//   - CreateBatch(10000) must succeed on every engine — the statement has to be
//     chunked to stay within each dialect's bind-parameter ceiling (MSSQL ~2100,
//     SQLite/PG/MySQL up to 32766/65535). This is the F4 spec's "chunking
//     automático" requirement.
//
// Sub-tests of a server engine share the engine's table, so each scopes its
// assertions to its own org_id namespace, never to absolute table counts.
//
// Scaled down from the spec (logged): the spec's 1M orders / 5M order_lines,
// the 2× peak-memory budget and the p50<50ms / p99<500ms latency budgets are
// F14 soak-tier. F4 seeds a few thousand rows — enough to exercise the cap,
// the streaming paths and the param-ceiling, not the latency envelope.
package f04_volume

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f04_volume"

// Volumes. readVolume feeds the read-path sub-tests (seeded in safe sub-batches
// so the seed itself never depends on the CreateBatch-chunking fix under test).
// batchVolume is inserted in a single CreateBatch call: with 7 bound columns
// that is 70_000 placeholders, over every engine's ceiling except Oracle (which
// takes the single-row INSERT loop), so the un-chunked path fails 5/6 engines.
const (
	readVolume  = 5000
	batchVolume = 10000
	seedChunk   = 500 // rows per CreateBatch call while seeding the read set
	orgRead     = 1   // org_id namespace for the read-path sub-tests
	orgBatch    = 2   // org_id namespace for the CreateBatch-chunking sub-test
)

var engineFlag = flag.String("engines", "sqlite",
	"comma-separated engines (sqlite,postgres,mysql,mariadb,mssql,oracle) or 'all'")

func selectedEngines() []string {
	v := strings.TrimSpace(*engineFlag)
	if v == "" || v == "all" {
		return tools.AllEngines
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// widget has 7 insertable columns (id is auto-increment and skipped on insert).
// The column count is load-bearing: batchVolume × 7 must exceed the bind-param
// ceiling so the un-chunked CreateBatch path is actually exercised.
type widget struct {
	ID       int64  `db:"id" pk:"true"`
	OrgID    int64  `db:"org_id"`
	Name     string `db:"name"`
	SKU      string `db:"sku"`
	Category string `db:"category"`
	Price    int64  `db:"price"`
	Qty      int    `db:"qty"`
	Active   bool   `db:"active"`
}

func allModels() []any { return []any{&widget{}} }

type rec struct {
	t   *testing.T
	eng string
	cat reporter.Category
}

func newRec(t *testing.T, eng string, cat reporter.Category) rec {
	return rec{t: t, eng: eng, cat: cat}
}

func (r rec) fail(name string, sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase: phase, Test: name, Engine: r.eng, Category: r.cat, Severity: sev,
		Error: fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestVolume ./phases/f04_volume/... -engines=" + r.eng,
		},
	})
}

func TestVolume(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var ce []string
		for _, e := range engines {
			if e != tools.SQLite {
				ce = append(ce, e)
			}
		}
		tools.Down(ce...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) {
			client := newClient(t, ctx, conn, eng)

			// Seed the read set once per engine, in safe sub-batches.
			if err := seedWidgets(ctx, client, orgRead, readVolume); err != nil {
				t.Fatalf("seed %d widgets on %s: %v", readVolume, eng, err)
			}

			t.Run("ListImplicitCap", func(t *testing.T) { listImplicitCap(t, ctx, client, eng) })
			t.Run("DeepOffsetPagination", func(t *testing.T) { deepOffsetPagination(t, ctx, client, eng) })
			t.Run("CursorFullScan", func(t *testing.T) { cursorFullScan(t, ctx, client, eng) })
			t.Run("IterEarlyStop", func(t *testing.T) { iterEarlyStop(t, ctx, client, eng) })
			t.Run("IterContextCancel", func(t *testing.T) { iterContextCancel(t, ctx, client, eng) })
			t.Run("PaginateExactCount", func(t *testing.T) { paginateExactCount(t, ctx, client, eng) })
			t.Run("CreateBatchChunking", func(t *testing.T) { createBatchChunking(t, ctx, client, eng) })
		})
	}
}

func newClient(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) *quark.Client {
	t.Helper()
	client, err := quark.New(conn.Driver, conn.DSN)
	if err != nil {
		t.Fatalf("quark.New(%q): %v", conn.Driver, err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		if eng == tools.SQLite {
			_ = os.Remove(conn.DSN)
		}
	})
	if err := client.Migrate(ctx, allModels()...); err != nil {
		t.Fatalf("migrate on %s: %v", eng, err)
	}
	return client
}

// seedWidgets inserts n widgets for org via CreateBatch in chunks of seedChunk.
// Chunking here is the test harness's own, independent of the fix under test:
// it keeps the seed valid on every engine even when the library path is not.
func seedWidgets(ctx context.Context, c *quark.Client, org int64, n int) error {
	for start := 0; start < n; start += seedChunk {
		end := start + seedChunk
		if end > n {
			end = n
		}
		batch := make([]*widget, 0, end-start)
		for i := start; i < end; i++ {
			batch = append(batch, newWidget(org, i))
		}
		if err := quark.For[widget](ctx, c).CreateBatch(batch); err != nil {
			return fmt.Errorf("seed chunk [%d,%d): %w", start, end, err)
		}
	}
	return nil
}

func newWidget(org int64, i int) *widget {
	return &widget{
		OrgID:    org,
		Name:     fmt.Sprintf("widget-%d-%d", org, i),
		SKU:      fmt.Sprintf("SKU-%d-%06d", org, i),
		Category: fmt.Sprintf("cat-%d", i%20),
		Price:    int64(100 + i),
		Qty:      i % 500,
		Active:   i%2 == 0,
	}
}

func orgQuery(ctx context.Context, c *quark.Client, org int64) *quark.Query[widget] {
	return quark.For[widget](ctx, c).Where("org_id", "=", org)
}

// listImplicitCap: List() without Limit() truncates at the documented safety
// cap of 100 (it must not return the full 5000), and an explicit Limit()
// overrides the cap and returns everything in the org namespace.
func listImplicitCap(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)

	capped, err := orgQuery(ctx, c, orgRead).List()
	if err != nil {
		r.fail("ListImplicitCap", reporter.SeverityP1, "List() without Limit: %v", err)
		return
	}
	if len(capped) != 100 {
		r.fail("ListImplicitCap", reporter.SeverityP1,
			"List() without Limit returned %d rows, want the documented safety cap of 100", len(capped))
	}

	full, err := orgQuery(ctx, c, orgRead).Limit(readVolume).List()
	if err != nil {
		r.fail("ListImplicitCap", reporter.SeverityP1, "List() with explicit Limit(%d): %v", readVolume, err)
		return
	}
	if len(full) != readVolume {
		r.fail("ListImplicitCap", reporter.SeverityP1,
			"List() with explicit Limit(%d) returned %d rows, want %d", readVolume, len(full), readVolume)
	}
}

// deepOffsetPagination: walk the org namespace with Limit(seedChunk)+Offset and
// assert the union of pages is exactly the full set — no gaps, no duplicates,
// strictly increasing ids.
func deepOffsetPagination(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	seen := make(map[int64]bool, readVolume)
	var lastID int64
	for offset := 0; offset < readVolume; offset += seedChunk {
		page, err := orgQuery(ctx, c, orgRead).
			OrderBy("id", "ASC").Limit(seedChunk).Offset(offset).List()
		if err != nil {
			r.fail("DeepOffsetPagination", reporter.SeverityP1, "page at offset %d: %v", offset, err)
			return
		}
		for _, w := range page {
			if seen[w.ID] {
				r.fail("DeepOffsetPagination", reporter.SeverityP1,
					"duplicate id %d across pages at offset %d", w.ID, offset)
				return
			}
			if w.ID <= lastID {
				r.fail("DeepOffsetPagination", reporter.SeverityP1,
					"non-increasing id %d after %d at offset %d (ordering not stable across pages)", w.ID, lastID, offset)
				return
			}
			seen[w.ID] = true
			lastID = w.ID
		}
	}
	if len(seen) != readVolume {
		r.fail("DeepOffsetPagination", reporter.SeverityP1,
			"offset walk covered %d distinct rows, want %d (gap or overlap)", len(seen), readVolume)
	}
}

// cursorFullScan: Cursor() streams the whole org namespace and closes cleanly.
func cursorFullScan(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	cur, err := orgQuery(ctx, c, orgRead).OrderBy("id", "ASC").Cursor()
	if err != nil {
		r.fail("CursorFullScan", reporter.SeverityP1, "open cursor: %v", err)
		return
	}
	count := 0
	var lastID int64
	for cur.Next() {
		var w widget
		if err := cur.Scan(&w); err != nil {
			r.fail("CursorFullScan", reporter.SeverityP1, "scan at row %d: %v", count, err)
			_ = cur.Close()
			return
		}
		if w.ID <= lastID {
			r.fail("CursorFullScan", reporter.SeverityP1, "non-increasing id %d after %d", w.ID, lastID)
			_ = cur.Close()
			return
		}
		lastID = w.ID
		count++
	}
	if err := cur.Err(); err != nil {
		r.fail("CursorFullScan", reporter.SeverityP1, "cursor err after scan: %v", err)
	}
	if err := cur.Close(); err != nil {
		r.fail("CursorFullScan", reporter.SeverityP1, "close cursor: %v", err)
	}
	if count != readVolume {
		r.fail("CursorFullScan", reporter.SeverityP1, "cursor yielded %d rows, want %d", count, readVolume)
	}
}

var errStopIter = errors.New("f04: stop iter")

// iterEarlyStop: Iter() must surface a callback error verbatim and stop at the
// row that returned it; an error-free Iter() must visit the whole set.
func iterEarlyStop(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)

	const stopAt = 10
	processed := 0
	err := orgQuery(ctx, c, orgRead).OrderBy("id", "ASC").Iter(func(widget) error {
		processed++
		if processed == stopAt {
			return errStopIter
		}
		return nil
	})
	if !errors.Is(err, errStopIter) {
		r.fail("IterEarlyStop", reporter.SeverityP1, "Iter() did not return the callback error, got %v", err)
	}
	if processed != stopAt {
		r.fail("IterEarlyStop", reporter.SeverityP1, "Iter() processed %d rows before stop, want %d", processed, stopAt)
	}

	full := 0
	if err := orgQuery(ctx, c, orgRead).Iter(func(widget) error { full++; return nil }); err != nil {
		r.fail("IterEarlyStop", reporter.SeverityP1, "full Iter(): %v", err)
	}
	if full != readVolume {
		r.fail("IterEarlyStop", reporter.SeverityP1, "full Iter() visited %d rows, want %d", full, readVolume)
	}
}

// iterContextCancel: cancelling the context mid-stream should stop Iter() before
// it drains the set and surface a non-nil error. This is driver-dependent — a
// driver that prefetches the full result set (or a small enough one) may visit
// every row before the cancellation is observed — so a failure here is a gap,
// not a regression. All five CI engines observe it at readVolume=5000.
func iterContextCancel(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng, reporter.CategoryGap)

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	const cancelAt = 10
	processed := 0
	err := orgQuery(cctx, c, orgRead).OrderBy("id", "ASC").Iter(func(widget) error {
		processed++
		if processed == cancelAt {
			cancel()
		}
		return nil
	})
	if err == nil {
		r.fail("IterContextCancel", reporter.SeverityP2,
			"Iter() drained %d rows after context cancellation without error; mid-stream cancellation not observed", processed)
		return
	}
	if processed >= readVolume {
		r.fail("IterContextCancel", reporter.SeverityP2,
			"Iter() returned %v but still visited all %d rows; cancellation not observed mid-stream", err, processed)
	}
}

// paginateExactCount: Paginate() reports the exact Total over the org namespace
// and a correct partial last page.
func paginateExactCount(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)

	const pageSize = 700
	wantPages := int64((readVolume + pageSize - 1) / pageSize) // 8
	wantLast := readVolume - int(wantPages-1)*pageSize         // 100

	first, err := orgQuery(ctx, c, orgRead).OrderBy("id", "ASC").Paginate(pageSize, 0)
	if err != nil {
		r.fail("PaginateExactCount", reporter.SeverityP1, "Paginate page 0: %v", err)
		return
	}
	if first.Total != readVolume {
		r.fail("PaginateExactCount", reporter.SeverityP1, "Paginate Total=%d, want %d (count not exact)", first.Total, readVolume)
	}
	if first.TotalPages != wantPages {
		r.fail("PaginateExactCount", reporter.SeverityP1, "Paginate TotalPages=%d, want %d", first.TotalPages, wantPages)
	}
	if len(first.Items) != pageSize {
		r.fail("PaginateExactCount", reporter.SeverityP1, "page 0 has %d items, want %d", len(first.Items), pageSize)
	}

	last, err := orgQuery(ctx, c, orgRead).OrderBy("id", "ASC").Paginate(pageSize, int(wantPages)-1)
	if err != nil {
		r.fail("PaginateExactCount", reporter.SeverityP1, "Paginate last page: %v", err)
		return
	}
	if len(last.Items) != wantLast {
		r.fail("PaginateExactCount", reporter.SeverityP1, "last page has %d items, want %d (partial page wrong)", len(last.Items), wantLast)
	}
}

// createBatchChunking: a single CreateBatch of batchVolume rows must succeed on
// every engine. batchVolume × 7 columns overruns the bind-param ceiling of
// MSSQL/SQLite/PG/MySQL, so the un-chunked statement fails there — this is the
// F4 finder for the missing chunking. After the fix, every row must land.
func createBatchChunking(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng, reporter.CategoryDialectSpecific)

	batch := make([]*widget, 0, batchVolume)
	for i := 0; i < batchVolume; i++ {
		batch = append(batch, newWidget(orgBatch, i))
	}
	if err := quark.For[widget](ctx, c).CreateBatch(batch); err != nil {
		r.fail("CreateBatchChunking", reporter.SeverityP1,
			"CreateBatch(%d) failed (statement not chunked to the dialect's bind-param ceiling): %v", batchVolume, err)
		return
	}

	got, err := orgQuery(ctx, c, orgBatch).Count()
	if err != nil {
		r.fail("CreateBatchChunking", reporter.SeverityP1, "count after CreateBatch: %v", err)
		return
	}
	if got != batchVolume {
		r.fail("CreateBatchChunking", reporter.SeverityP1, "CreateBatch persisted %d rows, want %d", got, batchVolume)
	}

	// Spot-check a row to confirm chunk boundaries did not corrupt column order.
	row, err := orgQuery(ctx, c, orgBatch).Where("sku", "=", fmt.Sprintf("SKU-%d-%06d", orgBatch, batchVolume-1)).First()
	if err != nil {
		r.fail("CreateBatchChunking", reporter.SeverityP1, "fetch last batched row: %v", err)
		return
	}
	if row.Price != int64(100+batchVolume-1) {
		r.fail("CreateBatchChunking", reporter.SeverityP1, "last batched row Price=%d, want %d", row.Price, 100+batchVolume-1)
	}
}

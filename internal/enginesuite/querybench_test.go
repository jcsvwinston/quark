// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// TestQueryBench runs the 60-query bench and asserts the RECORDED verdict of
// every case, not its success. A case whose verdict is qbNoAPI must still
// fail; when someone closes the gap, this test goes red and asks for the
// verdict — and docs/query-bench.md — to be updated. That is the point: the
// bench is the numerator of the A4 gate, and a numerator nobody has to
// maintain drifts away from the truth it claims to measure.
//
// It runs against SQLite so it stays cheap enough to be a gate. Cases whose
// answer depends on the engine carry a Note saying so, and the ones that need
// a real engine (family I) are covered by locking_test.go.
func TestQueryBench(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", "file:querybench?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &qbCategory{}, &qbProduct{}, &qbUser{}, &qbOrder{}, &qbOrderItem{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := qbSeed(ctx, client); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var typed, wrong, noAPI, skipped int
	for _, tc := range qbCases() {
		tc := tc
		t.Run(tc.ID, func(t *testing.T) {
			switch tc.Verdict {
			case qbTyped:
				typed++
			case qbWrong:
				wrong++
			default:
				noAPI++
			}
			if tc.Run == nil {
				// No spelling to run: the measurement is that the API has
				// none, recorded in Note.
				skipped++
				if tc.Note == "" {
					t.Errorf("%s has no Run and no Note: an unrunnable case must say why", tc.ID)
				}
				return
			}
			err := tc.Run(ctx, client)
			switch tc.Verdict {
			case qbTyped, qbWrong:
				// qbWrong cases DO execute — that is what makes them
				// dangerous. The defect is in the SQL, recorded in Note.
				if err != nil {
					t.Errorf("%s (%s) is recorded as %s but failed: %v\nwant: %s",
						tc.ID, tc.Family, tc.Verdict, err, tc.Want)
				}
			case qbNoAPI:
				if err == nil {
					t.Errorf("%s (%s) is recorded as no-api but SUCCEEDED — the gap is closed; "+
						"update the verdict and docs/query-bench.md\nwant: %s", tc.ID, tc.Family, tc.Want)
				}
			}
		})
	}

	total := typed + wrong + noAPI
	if total != 60 {
		t.Errorf("the bench must hold exactly 60 queries, found %d", total)
	}
	t.Logf("query bench: %d typed, %d wrong-sql, %d no-api (of %d); %d have no spelling to run",
		typed, wrong, noAPI, total, skipped)
}

func qbSeed(ctx context.Context, c *quark.Client) error {
	now := time.Now()
	if err := quark.For[qbUser](ctx, c).CreateBatch([]*qbUser{
		{ID: 1, Email: "a@example.test", Country: "ES", Profile: `{"tier":"gold"}`, CreatedAt: now},
		{ID: 2, Email: "b@example.test", Country: "FR", Profile: `{"tier":"free"}`, CreatedAt: now},
	}); err != nil {
		return err
	}
	// Four levels deep on purpose: a two-level tree cannot tell a recursive
	// CTE from a single join, so Q37 would pass without recursing.
	l1, l2, l3 := int64(1), int64(2), int64(3)
	if err := quark.For[qbCategory](ctx, c).CreateBatch([]*qbCategory{
		{ID: 1, Name: "root"},
		{ID: 2, Name: "child", ParentID: &l1},
		{ID: 3, Name: "grandchild", ParentID: &l2},
		{ID: 4, Name: "great-grandchild", ParentID: &l3},
	}); err != nil {
		return err
	}
	if err := quark.For[qbProduct](ctx, c).CreateBatch([]*qbProduct{
		{ID: 1, CategoryID: 1, Name: "p1", Price: 9.5, Stock: 3, Attrs: `{"dims":{"width":20}}`, Active: true},
		{ID: 2, CategoryID: 2, Name: "p2", Price: 99, Stock: 0, Attrs: `{"dims":{"width":5}}`, Active: false},
	}); err != nil {
		return err
	}
	if err := quark.For[qbOrder](ctx, c).CreateBatch([]*qbOrder{
		{ID: 1, UserID: 1, Total: 120, Status: "paid", PlacedAt: now},
		{ID: 2, UserID: 1, Total: 15, Status: "pending", PlacedAt: now},
		{ID: 3, UserID: 2, Total: 800, Status: "shipped", PlacedAt: now},
	}); err != nil {
		return err
	}
	return quark.For[qbOrderItem](ctx, c).CreateBatch([]*qbOrderItem{
		{ID: 1, OrderID: 1, ProductID: 1, Qty: 2, UnitPrice: 9.5},
		{ID: 2, OrderID: 3, ProductID: 2, Qty: 1, UnitPrice: 99},
	})
}

// qbSummary renders the verdict counts the way docs/query-bench.md quotes
// them, so the document can be regenerated instead of hand-edited.
func qbSummary() string {
	byFamily := map[string][3]int{}
	var order []string
	for _, tc := range qbCases() {
		if _, seen := byFamily[tc.Family]; !seen {
			order = append(order, tc.Family)
		}
		c := byFamily[tc.Family]
		c[int(tc.Verdict)]++
		byFamily[tc.Family] = c
	}
	var b strings.Builder
	b.WriteString("| family | typed | wrong-sql | no-api |\n|---|---|---|---|\n")
	for _, f := range order {
		c := byFamily[f]
		b.WriteString(strings.Join([]string{"| " + f, itoa(c[0]), itoa(c[1]), itoa(c[2]) + " |"}, " | "))
		b.WriteString("\n")
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// TestQueryBenchSummary prints the per-family table. Run it with -v to
// regenerate the table in docs/query-bench.md.
func TestQueryBenchSummary(t *testing.T) {
	t.Log("\n" + qbSummary())
}

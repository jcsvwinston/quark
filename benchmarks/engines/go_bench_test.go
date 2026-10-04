// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"strings"
	"testing"
)

// BenchmarkPostgres and BenchmarkMySQL run the same operations as
// TestEngineBench through `go test -bench`, one sub-benchmark per arm, for
// benchstat and for profiling one arm on its own:
//
//	go test -run '^$' -bench 'Postgres/List100/quark' -cpuprofile cpu.out
//
// They judge nothing; TestEngineBench is the bench. Without a DSN they skip,
// which is what the smoke lane's `-bench=. -benchtime=1x` sees.
func BenchmarkPostgres(b *testing.B) { benchEngine(b, engines()[0]) }

func BenchmarkMySQL(b *testing.B) { benchEngine(b, engines()[1]) }

func benchEngine(b *testing.B, e engine) {
	dsn := dsnFor(b, e)
	e.setup(b, dsn)
	ctx := context.Background()
	for _, op := range e.ops() {
		for _, a := range op.arms {
			b.Run(op.name+"/"+benchName(a.name), func(b *testing.B) {
				step := a.open(b, dsn)
				if op.reset != "" {
					admin := mustOpen(b, e.driver, dsn)
					defer admin.Close()
					if _, err := admin.ExecContext(ctx, op.reset); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := step(ctx, i); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// benchName makes an arm's published name usable in -bench patterns:
// "database/sql, statement reused" → "database-sql_statement-reused".
func benchName(s string) string {
	r := strings.NewReplacer("/", "-", ", ", "_", " ", "-", "=", "-")
	return r.Replace(s)
}

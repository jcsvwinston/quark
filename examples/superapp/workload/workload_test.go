package workload

import (
	"testing"
	"time"
)

// TestStatOf fija la definición de percentil usada en el informe (índice más
// cercano sobre datos ordenados) con valores conocidos.
func TestStatOf(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	s := statOf([]time.Duration{ms(10), ms(20), ms(30), ms(40), ms(50)})
	if s.Count != 5 {
		t.Fatalf("Count=%d, want 5", s.Count)
	}
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"p50", s.P50, 30}, {"p95", s.P95, 50}, {"p99", s.P99, 50}, {"max", s.Max, 50},
	} {
		if c.got != c.want {
			t.Errorf("%s=%.2f, want %.2f", c.name, c.got, c.want)
		}
	}

	// Bordes: vacío → cero-value; un elemento → ese valor en todos los percentiles.
	if z := statOf(nil); z.Count != 0 || z.P50 != 0 {
		t.Errorf("statOf(nil) = %+v, want zero", z)
	}
	if one := statOf([]time.Duration{7 * time.Millisecond}); one.P50 != 7 || one.P99 != 7 || one.Max != 7 {
		t.Errorf("statOf(single) = %+v, want 7 en todos", one)
	}
}

// TestSQLVerb verifica la clasificación SELECT vs RETURNING (la base de separar
// filas leídas de filas devueltas por INSERT…RETURNING).
func TestSQLVerb(t *testing.T) {
	for in, want := range map[string]string{
		`SELECT * FROM "t"`:                                 "SELECT",
		"  insert into t values (?)":                        "INSERT",
		"INSERT INTO \"t\" (a) VALUES (?) RETURNING \"id\"": "INSERT",
		"UPDATE t SET a = ?":                                "UPDATE",
		"\n\tDELETE FROM t":                                 "DELETE",
	} {
		if got := sqlVerb(in); got != want {
			t.Errorf("sqlVerb(%q) = %q, want %q", in, got, want)
		}
	}
}

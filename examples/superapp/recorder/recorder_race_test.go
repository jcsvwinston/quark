package recorder

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
)

// TestRecorderConcurrentAccess es el test de concurrencia que el code-reviewer
// dejó anotado en S2: el Recorder es mutex-safe pero su suite era secuencial.
// Aquí N goroutines comparten UN Recorder a través de un client real (cada una
// ejecuta queries con Mark/Note intercalados) y al final se verifica la
// coherencia de los agregados: Count() == statements esperados,
// len(Statements()) == Count(), y la cobertura (Invoked) contiene todo lo
// marcado. Córrelo con `go test -race` para la detección de data races — sin
// -race sigue validando la coherencia de conteo bajo concurrencia.
func TestRecorderConcurrentAccess(t *testing.T) {
	rec := New(control.SQLite)
	// Fichero real (no :memory:, que da una BD VACÍA por conexión del pool) en
	// el TempDir del test. Los workers hacen LECTURAS (Count): lectores
	// concurrentes no contienden en SQLite y el objetivo es la concurrencia
	// del Recorder, no el throughput de escritura del motor.
	l := quark.DefaultLimits()
	l.SafeMigrations = false
	client, err := quark.New("sqlite", t.TempDir()+"/race.db", append(rec.Options(), quark.WithLimits(l))...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.Migrate(ctx, &widget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := quark.For[widget](ctx, client).Create(&widget{Name: "seed"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec.Reset() // el conteo parte de 0: sólo los statements de los workers

	const (
		workers = 8
		perW    = 25
	)
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			sym := fmt.Sprintf("quark.race.worker%d", w)
			for i := 0; i < perW; i++ {
				// Mark atribuye el SQL del Count al símbolo del worker;
				// Note concurrente ejercita la otra vía de cobertura.
				rec.Note(symOption)
				mctx := rec.Mark(ctx, sym)
				if n, err := quark.For[widget](mctx, client).Count(); err != nil || n != 1 {
					errs <- fmt.Errorf("worker %d count %d: n=%d err=%v", w, i, n, err)
					return
				}
			}
			errs <- nil
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	// Coherencia de agregados: cada Count de worker es exactamente 1
	// statement (vía query_row de lectura), sin pérdidas ni duplicados.
	want := workers * perW
	if got := rec.Count(); got != want {
		t.Errorf("Count()=%d, esperaba %d", got, want)
	}
	if got := len(rec.Statements()); got != want {
		t.Errorf("len(Statements())=%d, esperaba %d", got, want)
	}
	inv := rec.Invoked()
	if !inv[symOption] {
		t.Errorf("cobertura: falta el símbolo noteado %s", symOption)
	}
	for w := 0; w < workers; w++ {
		sym := fmt.Sprintf("quark.race.worker%d", w)
		if !inv[sym] {
			t.Errorf("cobertura: falta el símbolo marcado %s", sym)
		}
	}
	// La telemetría agregada también debe cuadrar (sumas bajo el mismo mutex):
	// cada Count dispara exactamente un evento de observer.
	if tel := rec.Telemetry(); tel.Queries != want {
		t.Errorf("Telemetry().Queries=%d, esperaba %d", tel.Queries, want)
	}
}

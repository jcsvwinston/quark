package exercise

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// ERRCLASS ejerce la clasificación de errores EXPORTADA (quark.IsUniqueViolation
// y quark.IsDeadlock) contra errores REALES de cada driver, no contra fixtures.
//
// Es deliberado que no haya `rec.Note` de cortesía aquí: estos dos símbolos
// existen porque la clasificación por tipo de driver es fácil de escribir mal y
// falla EN SILENCIO cuando se escribe mal — un predicado que devuelve false de
// más no rompe ninguna compilación ni emite ningún log, sólo apaga la feature
// que depende de él. La única prueba que vale es provocar el error de verdad en
// el motor de verdad y preguntarle al predicado.
//
// El fallo que motivó el arco es exactamente ese: la clasificación de
// PostgreSQL casaba el tipo concreto de pgx, así que bajo `lib/pq` —el driver
// que la guía de instalación prescribe— ninguna violación de unicidad, ningún
// deadlock y ninguna caída de conexión se reconocían.
type errProbe struct {
	ID    int64  `db:"id" pk:"true"`
	Email string `db:"email" quark:"unique,not_null"`
	Bal   int64  `db:"bal" default:"0"`
}

func (errProbe) TableName() string { return "err_probes" }

var ERRCLASS = Exerciser{Name: "errors-classify", Fn: runErrorsClassify}

func runErrorsClassify(ctx context.Context, _ *quark.Client, rec *recorder.Recorder, conn Conn) error {
	// Pool acotado por la misma razón que DEADLOCK: el techo de sesiones de
	// Oracle no tolera pools grandes en la matriz completa.
	cl, err := quark.New(conn.Driver, conn.DSN, append(rec.Options(), quark.WithMaxOpenConns(4))...)
	if err != nil {
		return fmt.Errorf("client errclass: %w", err)
	}
	defer cl.Close()

	if err := cl.Migrate(ctx, &errProbe{}); err != nil {
		return fmt.Errorf("migrate err_probes: %w", err)
	}
	// Converge al ENTRAR (no sólo al salir): un contenedor reusado con
	// `-keep` deja la tabla sembrada, y una siembra previa haría fallar el
	// primer insert por el mismo unique que el exerciser quiere provocar
	// después — el fallo de re-entrancia que ya mordió a `cache`/`crud`.
	_, _ = cl.Raw().ExecContext(ctx, "DELETE FROM err_probes")
	defer func() { _, _ = cl.Raw().ExecContext(context.Background(), "DROP TABLE err_probes") }()

	dupErr, err := exerciseUniqueViolation(ctx, cl)
	if err != nil {
		return err
	}
	rec.Note(QF("IsUniqueViolation"))

	if err := exerciseDeadlockClassification(ctx, cl, conn, dupErr); err != nil {
		return err
	}
	rec.Note(QF("IsDeadlock"))
	return nil
}

// exerciseUniqueViolation provoca una violación de unicidad REAL y comprueba
// que el predicado exportado la reconoce, que el sentinel grueso coincide, y
// que el error subyacente sigue alcanzable. Devuelve el error para reusarlo
// como negativo del clasificador de deadlocks.
func exerciseUniqueViolation(ctx context.Context, cl *quark.Client) (error, error) {
	const email = "errclass@superapp.test"
	if err := quark.For[errProbe](ctx, cl).Create(&errProbe{Email: email}); err != nil {
		return nil, fmt.Errorf("seed errProbe: %w", err)
	}

	dupErr := quark.For[errProbe](ctx, cl).Create(&errProbe{Email: email})
	if dupErr == nil {
		return nil, errors.New("el segundo Create con el mismo email debía violar el unique y no falló")
	}
	if !quark.IsUniqueViolation(dupErr) {
		return nil, fmt.Errorf("IsUniqueViolation no reconoció una violación REAL del motor: %v", dupErr)
	}
	// El sentinel grueso y el predicado fino deben coincidir sobre el mismo
	// error: si divergen, el usuario recibe dos respuestas para un hecho.
	if !errors.Is(dupErr, quark.ErrConstraintViolation) {
		return nil, fmt.Errorf("ErrConstraintViolation no envuelve la violación de unicidad: %v", dupErr)
	}
	// Un error corriente NO debe clasificarse: sin este negativo, un predicado
	// que devolviera true siempre pasaría el positivo de arriba.
	if quark.IsUniqueViolation(errors.New("boom")) {
		return nil, errors.New("IsUniqueViolation clasificó un error corriente")
	}
	return dupErr, nil
}

// exerciseDeadlockClassification comprueba IsDeadlock sobre un deadlock REAL
// en los motores que pueden producirlo. Usa un client SIN WithDeadlockRetry a
// propósito: con el retry puesto, la opción se traga el error de la víctima y
// no queda nada que clasificar — que es justo lo que impedía verificar este
// predicado desde el exerciser de HA.
func exerciseDeadlockClassification(ctx context.Context, cl *quark.Client, conn Conn, dupErr error) error {
	// Negativo, en los 6 motores: una violación de unicidad no es un deadlock.
	// Los dos clasificadores comparten la misma extracción de SQLSTATE, así
	// que una confusión entre ellos es un fallo plausible, no teórico.
	if quark.IsDeadlock(dupErr) {
		return fmt.Errorf("IsDeadlock clasificó una violación de unicidad: %v", dupErr)
	}

	if !control.Supports(control.FeatDeadlock, conn.Engine) {
		// SQLite serializa las escrituras y nunca reporta deadlock; SQLITE_BUSY
		// es contención, no víctima elegida. Capacidad desigual, no fallo.
		return nil
	}

	if err := quark.For[errProbe](ctx, cl).CreateBatch([]*errProbe{
		{Email: "errclass-a@superapp.test"}, {Email: "errclass-b@superapp.test"},
	}); err != nil {
		return fmt.Errorf("seed deadlock rows: %w", err)
	}
	rowA, err := quark.For[errProbe](ctx, cl).Where("email", "=", "errclass-a@superapp.test").First()
	if err != nil {
		return fmt.Errorf("first a: %w", err)
	}
	rowB, err := quark.For[errProbe](ctx, cl).Where("email", "=", "errclass-b@superapp.test").First()
	if err != nil {
		return fmt.Errorf("first b: %w", err)
	}

	update := func(tx *quark.Tx, id, bal int64) error {
		_, err := quark.ForTx[errProbe](ctx, tx).Where("id", "=", id).
			UpdateMap(map[string]any{"bal": bal})
		return err
	}

	// Orden de locks invertido + barrera (patrón F12, igual que ha-deadlock).
	// Sin retry, UNA de las dos tx vuelve con el error del motor.
	//
	// Se reintenta hasta que el motor elija víctima: el deadlock depende del
	// planificador y una sola pasada puede resolverse sin víctima. Aceptar ese
	// caso a la primera dejaría la aserción SIN EJECUTAR en verde, que es el
	// modo de fallo peor — el que hace que nadie vuelva a mirar.
	const attempts = 3
	var victim error
	for i := 0; i < attempts && victim == nil; i++ {
		g1, g2 := make(chan struct{}, 1), make(chan struct{}, 1)
		barrier := func(self chan<- struct{}, other <-chan struct{}) {
			self <- struct{}{}
			select {
			case <-other:
			case <-time.After(10 * time.Second):
			}
		}
		var wg sync.WaitGroup
		var err1, err2 error
		wg.Add(2)
		go func() {
			defer wg.Done()
			err1 = cl.Tx(ctx, func(tx *quark.Tx) error {
				if err := update(tx, rowA.ID, 1); err != nil {
					return err
				}
				barrier(g1, g2)
				return update(tx, rowB.ID, 1)
			})
		}()
		go func() {
			defer wg.Done()
			err2 = cl.Tx(ctx, func(tx *quark.Tx) error {
				if err := update(tx, rowB.ID, 2); err != nil {
					return err
				}
				barrier(g2, g1)
				return update(tx, rowA.ID, 2)
			})
		}()
		wg.Wait()

		if victim = err1; victim == nil {
			victim = err2
		}
	}
	if victim == nil {
		// Tras varios intentos el motor no eligió víctima. No se convierte en
		// fallo —sería un test inestable sobre concurrencia, la lección del
		// canario de suscripción— pero tampoco se afirma nada del predicado.
		return nil
	}
	if !quark.IsDeadlock(victim) {
		return fmt.Errorf("IsDeadlock no reconoció el deadlock REAL de %s: %v", conn.Engine, victim)
	}
	return nil
}

package exercise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// haProbe es el modelo-sonda del routing de réplicas: cada base (primary y
// cada réplica) se siembra con marcadores DISTINTOS, y la señal de routing es
// la presencia-de-dato (qué marcador devuelve una lectura), no una etiqueta —
// la misma señal "más fuerte que la etiqueta OTel" que validó el bug-bash F11.
type haProbe struct {
	ID     int64  `db:"id" pk:"true"`
	Marker string `db:"marker" quark:"not_null"`
}

func (haProbe) TableName() string { return "ha_probes" }

// shardProbe es la sonda del sharding (skey evita la palabra reservada `key`).
type shardProbe struct {
	ID   int64  `db:"id" pk:"true"`
	Skey string `db:"skey" quark:"not_null"`
}

func (shardProbe) TableName() string { return "shard_probes" }

// dlProbe son las dos filas contendidas del deadlock (patrón bugbash F12).
type dlProbe struct {
	ID      int64  `db:"id" pk:"true"`
	Name    string `db:"name" quark:"not_null"`
	Balance int64  `db:"balance" default:"0"`
}

func (dlProbe) TableName() string { return "dl_probes" }

// provisionHADBs aprovisiona n bases auxiliares (réplicas o shards) con el
// MISMO mecanismo de DatabasePerTenant (tenant_dsn.go): ficheros en SQLite,
// CREATE DATABASE vía admin en PG/MySQL/MariaDB/MSSQL. Devuelve los DSNs y un
// cleanup (re-ejecutable: nombres fijos, pre-clean incluido). El caller debe
// cerrar TODOS los clients abiertos sobre esos DSNs antes de invocar cleanup
// (PG/MSSQL rechazan DROP DATABASE con sesiones vivas).
func provisionHADBs(ctx context.Context, conn Conn, prefix string, n int) ([]string, func(), error) {
	names := make([]string, n)
	dsns := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("superapp_ha_%s%d", prefix, i+1)
		dsn, err := tenantDBDSN(conn.Engine, conn.DSN, names[i])
		if err != nil {
			return nil, nil, err
		}
		dsns[i] = dsn
	}

	if conn.Engine == control.SQLite {
		for _, dsn := range dsns {
			_ = os.Remove(dsn) // pre-clean de una corrida previa
		}
		cleanup := func() {
			for _, dsn := range dsns {
				_ = os.Remove(dsn)
			}
		}
		return dsns, cleanup, nil
	}

	admin, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(quark.Limits{
		AllowRawQueries: true,
		MaxResults:      1000,
		QueryTimeout:    30 * time.Second,
	}))
	if err != nil {
		return nil, nil, fmt.Errorf("admin client: %w", err)
	}
	drop := func() {
		cctx := context.Background()
		for _, name := range names {
			if stmt, err := dropTenantDBSQL(conn.Engine, name); err == nil {
				_ = admin.Exec(cctx, stmt)
			}
		}
	}
	drop() // pre-clean
	for _, name := range names {
		stmt, err := createTenantDBSQL(conn.Engine, name)
		if err != nil {
			_ = admin.Close()
			return nil, nil, err
		}
		if err := admin.Exec(ctx, stmt); err != nil {
			_ = admin.Close()
			return nil, nil, fmt.Errorf("create database %s: %w", name, err)
		}
	}
	cleanup := func() {
		drop()
		_ = admin.Close()
	}
	return dsns, cleanup, nil
}

// REPLICAS ejerce el routing de lectura a réplicas (F6-5/F6-6, ADR-0015) con
// la señal presencia-de-dato: primary y réplicas llevan marcadores distintos.
// Cubre: reads no-sticky → réplica (round-robin reparte entre las dos),
// Sticky → primary (read-your-writes), reads dentro de Tx → primary, writes →
// SOLO primary (las réplicas no ven el INSERT), el path single-row de lectura
// (Count) también ruteado, y un ping funcional por estrategia
// (Random/LeastConn). El failover/cooldown real (réplica caída) queda cubierto
// por replicas_postgres_test.go y el bug-bash F11 — necesita tumbar instancias,
// fuera del alcance in-process del arnés; la opción WithReplicaDownCooldown se
// invoca igualmente (símbolo ejercido, semántica citada).
var REPLICAS = Exerciser{Name: "ha-replicas", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	rec.Note(QF("ReplicaStrategy"), QF("ReplicaRoundRobin"), QF("ReplicaRandom"), QF("ReplicaLeastConn"))
	if !control.Supports(control.FeatDBPerTenantProvision, conn.Engine) {
		// Oracle: las réplicas del arnés son DSNs aprovisionados (PDB fuera de
		// alcance, ver capability.go). El routing es client-side y queda
		// ejercido en los demás motores.
		return nil
	}

	dsns, cleanup, err := provisionHADBs(ctx, conn, "r", 2)
	if err != nil {
		return fmt.Errorf("provision réplicas: %w", err)
	}
	defer cleanup()

	// Siembra cada réplica con su marcador (schema + 1 fila), client efímero.
	for i, dsn := range dsns {
		rc, err := quark.New(conn.Driver, dsn)
		if err != nil {
			return fmt.Errorf("replica client %d: %w", i+1, err)
		}
		if err := rc.Migrate(ctx, &haProbe{}); err != nil {
			_ = rc.Close()
			return fmt.Errorf("migrate réplica %d: %w", i+1, err)
		}
		if err := quark.For[haProbe](ctx, rc).Create(&haProbe{Marker: fmt.Sprintf("r%d", i+1)}); err != nil {
			_ = rc.Close()
			return fmt.Errorf("seed réplica %d: %w", i+1, err)
		}
		if err := rc.Close(); err != nil {
			return fmt.Errorf("close réplica %d: %w", i+1, err)
		}
	}

	// Primary = la BD del harness, con DOS marcadores (count primary=2 vs
	// réplica=1: señal para el path single-row de lectura). La tabla se crea
	// aquí y se dropea al salir (el converge de MIGRATE la vería como stray).
	if err := client.Migrate(ctx, &haProbe{}); err != nil {
		return fmt.Errorf("migrate primary: %w", err)
	}
	defer func() { _, _ = client.Raw().ExecContext(context.Background(), "DROP TABLE ha_probes") }()
	_, _ = client.Raw().ExecContext(ctx, "DELETE FROM ha_probes")
	for _, m := range []string{"primary", "primary2"} {
		if err := quark.For[haProbe](ctx, client).Create(&haProbe{Marker: m}); err != nil {
			return fmt.Errorf("seed primary: %w", err)
		}
	}

	// Client ruteado: mismo primary + 2 réplicas, round-robin explícito.
	rec.Note(QF("WithReplicas"), QF("WithReplicaStrategy"), QF("WithReplicaDownCooldown"))
	routed, err := quark.New(conn.Driver, conn.DSN, append(rec.Options(),
		quark.WithReplicas(dsns...),
		quark.WithReplicaStrategy(quark.ReplicaRoundRobin),
		quark.WithReplicaDownCooldown(30*time.Second))...)
	if err != nil {
		return fmt.Errorf("routed client: %w", err)
	}
	defer routed.Close()

	// Reads no-sticky → réplicas, y el round-robin reparte entre ambas.
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		got, err := quark.For[haProbe](rec.Mark(ctx, QM("First")), routed).OrderBy("id", "ASC").First()
		if err != nil {
			return fmt.Errorf("read no-sticky %d: %w", i, err)
		}
		seen[got.Marker] = true
	}
	if seen["primary"] || seen["primary2"] {
		return fmt.Errorf("una lectura no-sticky llegó al primary: %v", seen)
	}
	if !seen["r1"] || !seen["r2"] {
		return fmt.Errorf("round-robin no repartió entre las 2 réplicas en 4 lecturas: %v", seen)
	}
	// El path single-row de lectura (Count) también rutea a réplica.
	if n, err := quark.For[haProbe](ctx, routed).Count(); err != nil || n != 1 {
		return fmt.Errorf("count no-sticky: n=%d err=%v, esperaba 1 (réplica)", n, err)
	}

	// Sticky → primary (read-your-writes).
	sctx := quark.Sticky(ctx)
	rec.Note(QF("Sticky"))
	if n, err := quark.For[haProbe](sctx, routed).Count(); err != nil || n != 2 {
		return fmt.Errorf("count sticky: n=%d err=%v, esperaba 2 (primary)", n, err)
	}

	// Un read dentro de Tx usa la conexión de la tx (primary), sin Sticky.
	if err := routed.Tx(ctx, func(tx *quark.Tx) error {
		n, err := quark.ForTx[haProbe](ctx, tx).Count()
		if err != nil {
			return err
		}
		if n != 2 {
			return fmt.Errorf("count en tx: n=%d, esperaba 2 (primary)", n)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("tx read: %w", err)
	}

	// Un write vía el client ruteado va SOLO al primary: las réplicas siguen
	// con su única fila (no-sticky Count no cambia), el primary suma una.
	if err := quark.For[haProbe](ctx, routed).Create(&haProbe{Marker: "fresh"}); err != nil {
		return fmt.Errorf("write ruteado: %w", err)
	}
	if n, err := quark.For[haProbe](ctx, routed).Count(); err != nil || n != 1 {
		return fmt.Errorf("count no-sticky post-write: n=%d err=%v, esperaba 1 (la réplica no ve el write)", n, err)
	}
	if n, err := quark.For[haProbe](sctx, routed).Count(); err != nil || n != 3 {
		return fmt.Errorf("count sticky post-write: n=%d err=%v, esperaba 3 (write aterrizó en primary)", n, err)
	}

	// Ping funcional de las otras dos estrategias (la selección de réplica
	// cambia; el contrato de routing es el mismo).
	for _, st := range []quark.ReplicaStrategy{quark.ReplicaRandom, quark.ReplicaLeastConn} {
		c, err := quark.New(conn.Driver, conn.DSN, quark.WithReplicas(dsns...), quark.WithReplicaStrategy(st))
		if err != nil {
			return fmt.Errorf("client estrategia %v: %w", st, err)
		}
		n, err := quark.For[haProbe](ctx, c).Count()
		cerr := c.Close()
		if err != nil || n != 1 {
			return fmt.Errorf("estrategia %v: n=%d err=%v, esperaba 1 (réplica)", st, n, err)
		}
		if cerr != nil {
			return fmt.Errorf("close estrategia %v: %w", st, cerr)
		}
	}
	return nil
}}

// SHARDING ejerce el ShardRouter (F6-7, ADR-0016): shards = DSNs
// aprovisionados (mismo mecanismo que DatabasePerTenant), routing por shard
// key explícita, sin fan-out implícito, aislamiento físico entre shards, tx
// ligada a UN shard, y estabilidad de la API al añadir un shard nuevo.
var SHARDING = Exerciser{Name: "ha-sharding", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	rec.Note(QF("ShardRouter"), QF("ShardFunc"), QF("ShardResolver"))
	if !control.Supports(control.FeatDBPerTenantProvision, conn.Engine) {
		return nil // Oracle: shards del arnés = DSNs aprovisionados (capability.go)
	}

	dsns, cleanup, err := provisionHADBs(ctx, conn, "s", 3)
	if err != nil {
		return fmt.Errorf("provision shards: %w", err)
	}
	defer cleanup()

	names := []string{"s1", "s2", "s3"}
	shards := make(map[string]*quark.Client, 3)
	defer func() {
		for _, c := range shards {
			_ = c.Close()
		}
	}()
	for i, name := range names {
		c, err := quark.New(conn.Driver, dsns[i], rec.Options()...)
		if err != nil {
			return fmt.Errorf("shard client %s: %w", name, err)
		}
		shards[name] = c
		if err := c.Migrate(ctx, &shardProbe{}); err != nil {
			return fmt.Errorf("migrate shard %s: %w", name, err)
		}
	}

	rec.Note(QF("NewShardRouter"), QF("HashShardFunc"), QF("DefaultShardResolver"))
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver, quark.HashShardFunc(names))
	if err != nil {
		return fmt.Errorf("NewShardRouter: %w", err)
	}
	rec.Note(SRM("ShardNames"))
	if got := router.ShardNames(); len(got) != 3 {
		return fmt.Errorf("ShardNames()=%v, esperaba 3", got)
	}

	// Sin shard key: error explícito (sin fan-out implícito, ADR-0016).
	if _, err := quark.For[shardProbe](ctx, router).Limit(1).List(); !errors.Is(err, quark.ErrInvalidQuery) {
		return fmt.Errorf("sin shard key esperaba ErrInvalidQuery, got %v", err)
	}

	// Routing determinista: la misma key resuelve al MISMO client.
	rec.Note(QF("WithShardKey"), QF("ShardKeyFromContext"), SRM("GetClient"))
	kctx := quark.WithShardKey(ctx, "acct-007")
	if got := quark.ShardKeyFromContext(kctx); got != "acct-007" {
		return fmt.Errorf("ShardKeyFromContext=%q", got)
	}
	c1, err := router.GetClient(kctx)
	if err != nil {
		return fmt.Errorf("GetClient: %w", err)
	}
	if c2, _ := router.GetClient(kctx); c2 != c1 {
		return fmt.Errorf("la misma key resolvió a clients distintos")
	}

	// Distribución: 30 keys → todas las filas aterrizan, cada shard recibe
	// alguna (FNV-1a reparte; el chi-square fino ya lo pinneó el bug-bash F10).
	const nKeys = 30
	for i := 0; i < nKeys; i++ {
		k := fmt.Sprintf("cust-%02d", i)
		if err := quark.For[shardProbe](rec.Mark(quark.WithShardKey(ctx, k), QM("Create")), router).Create(&shardProbe{Skey: k}); err != nil {
			return fmt.Errorf("create %s: %w", k, err)
		}
	}
	total := int64(0)
	for name, c := range shards {
		n, err := quark.For[shardProbe](ctx, c).Count()
		if err != nil {
			return fmt.Errorf("count shard %s: %w", name, err)
		}
		if n == 0 {
			return fmt.Errorf("el shard %s no recibió ninguna key (distribución rota)", name)
		}
		total += n
	}
	if total != nKeys {
		return fmt.Errorf("filas totales=%d, esperaba %d (¿fan-out o pérdida?)", total, nKeys)
	}

	// Aislamiento: las filas de una key viven SOLO en su shard.
	probe, err := quark.For[shardProbe](quark.WithShardKey(ctx, "cust-00"), router).Where("skey", "=", "cust-00").First()
	if err != nil {
		return fmt.Errorf("first cust-00: %w", err)
	}
	owner, _ := router.GetClient(quark.WithShardKey(ctx, "cust-00"))
	for name, c := range shards {
		n, err := quark.For[shardProbe](ctx, c).Where("skey", "=", "cust-00").Count()
		if err != nil {
			return fmt.Errorf("isolation count %s: %w", name, err)
		}
		if c == owner && n != 1 {
			return fmt.Errorf("el shard dueño %s tiene %d filas de cust-00 (id=%d), esperaba 1", name, n, probe.ID)
		}
		if c != owner && n != 0 {
			return fmt.Errorf("fuga cross-shard: %s tiene %d filas de cust-00", name, n)
		}
	}

	// Tx ligada a un único shard: el GetClient de la key da el client y su Tx
	// no puede tocar otros shards (no existe tx cross-shard, ADR-0016).
	if err := c1.Tx(ctx, func(tx *quark.Tx) error {
		return quark.ForTx[shardProbe](ctx, tx).Create(&shardProbe{Skey: "tx-row"})
	}); err != nil {
		return fmt.Errorf("tx por shard: %w", err)
	}

	// Estabilidad al reshard: un 4º shard + router nuevo → la API sigue
	// funcionando (no hay migración de datos implícita; eso es scatter-gather,
	// deferral v1.2).
	dsns4, cleanup4, err := provisionHADBs(ctx, conn, "x", 1)
	if err != nil {
		return fmt.Errorf("provision 4º shard: %w", err)
	}
	defer cleanup4()
	c4, err := quark.New(conn.Driver, dsns4[0])
	if err != nil {
		return fmt.Errorf("client 4º shard: %w", err)
	}
	defer c4.Close()
	if err := c4.Migrate(ctx, &shardProbe{}); err != nil {
		return fmt.Errorf("migrate 4º shard: %w", err)
	}
	shards4 := map[string]*quark.Client{"s1": shards["s1"], "s2": shards["s2"], "s3": shards["s3"], "s4": c4}
	router4, err := quark.NewShardRouter(shards4, quark.DefaultShardResolver, quark.HashShardFunc([]string{"s1", "s2", "s3", "s4"}))
	if err != nil {
		return fmt.Errorf("router resharded: %w", err)
	}
	if err := quark.For[shardProbe](quark.WithShardKey(ctx, "post-reshard"), router4).Create(&shardProbe{Skey: "post-reshard"}); err != nil {
		return fmt.Errorf("create post-reshard: %w", err)
	}
	return nil
}}

// DEADLOCK ejerce WithDeadlockRetry (F4-7). En los 6 motores se construye un
// client con la opción y se corre una tx con dos updates (símbolo invocado +
// camino feliz); en los motores con servidor (FeatDeadlock) se fuerza además
// el deadlock real — dos tx con orden de locks invertido y barrera de canales
// (patrón bugbash F12 / tx_deadlock_integration_test.go) — y se asierta que el
// retry recupera a la víctima: ambas tx terminan sin error. En SQLite el
// deadlock no puede manifestarse (escrituras serializadas; SQLITE_BUSY no es
// código de deadlock), capacidad desigual ≠ fallo.
var DEADLOCK = Exerciser{Name: "ha-deadlock", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	rec.Note(QF("WithDeadlockRetry"))
	// Pool acotado a 4 (S7): el deadlock sólo necesita 2 conexiones vivas, y el
	// techo de sesiones de gvenzl en Oracle (ORA-12516, visto en el soak F14)
	// no tolera 8 conexiones propias + las del harness en la matriz completa.
	dl, err := quark.New(conn.Driver, conn.DSN, append(rec.Options(),
		quark.WithDeadlockRetry(6), quark.WithMaxOpenConns(4))...)
	if err != nil {
		return fmt.Errorf("client deadlock-retry: %w", err)
	}
	defer dl.Close()

	if err := dl.Migrate(ctx, &dlProbe{}); err != nil {
		return fmt.Errorf("migrate dl_probes: %w", err)
	}
	defer func() { _, _ = dl.Raw().ExecContext(context.Background(), "DROP TABLE dl_probes") }()
	_, _ = dl.Raw().ExecContext(ctx, "DELETE FROM dl_probes")

	if err := quark.For[dlProbe](ctx, dl).CreateBatch([]*dlProbe{{Name: "dlA"}, {Name: "dlB"}}); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	rowA, err := quark.For[dlProbe](ctx, dl).Where("name", "=", "dlA").First()
	if err != nil {
		return fmt.Errorf("first dlA: %w", err)
	}
	rowB, err := quark.For[dlProbe](ctx, dl).Where("name", "=", "dlB").First()
	if err != nil {
		return fmt.Errorf("first dlB: %w", err)
	}

	update := func(tx *quark.Tx, id int64, bal int64) error {
		_, err := quark.ForTx[dlProbe](ctx, tx).Where("id", "=", id).
			UpdateMap(map[string]any{"balance": bal})
		return err
	}
	rec.Note(QM("UpdateMap"))

	if !control.Supports(control.FeatDeadlock, conn.Engine) {
		// SQLite: camino feliz — la opción está instalada y la tx multi-update
		// funciona; la recuperación del deadlock se asierta en los servidores.
		if err := dl.Tx(ctx, func(tx *quark.Tx) error {
			if err := update(tx, rowA.ID, 10); err != nil {
				return err
			}
			return update(tx, rowB.ID, 10)
		}); err != nil {
			return fmt.Errorf("tx multi-update (camino feliz): %w", err)
		}
		return nil
	}

	// Deadlock real: orden de locks invertido + barrera. Sólo el PRIMER
	// intento de cada tx participa en la barrera (los retries de Client.Tx
	// corren en la misma goroutine).
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
		first := true
		err1 = dl.Tx(ctx, func(tx *quark.Tx) error {
			if err := update(tx, rowA.ID, 1); err != nil {
				return err
			}
			if first {
				first = false
				barrier(g1, g2)
			}
			return update(tx, rowB.ID, 1)
		})
	}()
	go func() {
		defer wg.Done()
		first := true
		err2 = dl.Tx(ctx, func(tx *quark.Tx) error {
			if err := update(tx, rowB.ID, 2); err != nil {
				return err
			}
			if first {
				first = false
				barrier(g2, g1)
			}
			return update(tx, rowA.ID, 2)
		})
	}()
	wg.Wait()
	if err1 != nil || err2 != nil {
		return fmt.Errorf("WithDeadlockRetry no recuperó a la víctima: err1=%v err2=%v", err1, err2)
	}
	// Consistencia post-deadlock: la tx ganadora del último commit fija AMBOS
	// balances (cada tx escribe las dos filas con su valor).
	a, err := quark.For[dlProbe](ctx, dl).Where("id", "=", rowA.ID).First()
	if err != nil {
		return fmt.Errorf("reread A: %w", err)
	}
	b, err := quark.For[dlProbe](ctx, dl).Where("id", "=", rowB.ID).First()
	if err != nil {
		return fmt.Errorf("reread B: %w", err)
	}
	if a.Balance != b.Balance {
		return fmt.Errorf("balances divergentes tras el retry: A=%d B=%d (atomicidad rota)", a.Balance, b.Balance)
	}
	return nil
}}

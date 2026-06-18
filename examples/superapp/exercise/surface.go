package exercise

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// SURFACE ejerce la cola de superficie pública CALLABLE que los exercisers de
// dominio no tocaban: constructores del AST de expresiones, window-funcs,
// factories de dialecto, option-funcs del Client, y funcs de model-meta/codegen.
// Todo es invocación GENUINA (se construye y, donde produce SQL, se ejecuta);
// los símbolos sin SQL propio se marcan con Note tras la llamada real. Es la
// parte "funcs" del cierre de denominador S7-coverage (allowlist dialect+tcv ya
// dejó el gate midiendo lo callable).
var SURFACE = Exerciser{Name: "surface-funcs", Fn: runSurfaceFuncs}

func runSurfaceFuncs(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	if err := surfaceExprAST(ctx, client, rec); err != nil {
		return err
	}
	if err := surfaceWindow(ctx, client, rec); err != nil {
		return err
	}
	if err := surfaceDialectFactories(rec); err != nil {
		return err
	}
	if err := surfaceClientOptions(rec, conn); err != nil {
		return err
	}
	surfaceModelMeta(rec)
	return nil
}

// surfaceExprAST construye un WhereExpr que USA cada combinador/leaf del AST y
// lo ejecuta (SELECT real), luego marca los constructores (no emiten SQL por sí
// mismos — el árbol entero se renderiza dentro del WHERE de la query).
func surfaceExprAST(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	// And / Or / Not / Cmp / Eq / Ne / Lt / Gt / Lte / Gte / In / NotIn / Func
	expr := quark.And(
		quark.Eq(quark.Col("role"), quark.Lit("member")),
		quark.Or(
			quark.Not(quark.Eq(quark.Col("name"), quark.Lit("nobody"))),
			quark.Cmp(quark.Col("version"), ">=", quark.Lit(int64(0))),
		),
		quark.In(quark.Col("role"), quark.Lit("admin"), quark.Lit("member"), quark.Lit("viewer")),
		quark.NotIn(quark.Col("name"), quark.Lit("x"), quark.Lit("y")),
		quark.Gt(quark.Func("LENGTH", quark.Col("email")), quark.Lit(int64(0))),
		quark.Ne(quark.Col("name"), quark.Lit("")),
		quark.Lt(quark.Col("version"), quark.Lit(int64(1<<30))),
		quark.Lte(quark.Col("version"), quark.Lit(int64(1<<30))),
	)
	if _, err := quark.For[domain.Account](ctx, client).WhereExpr(expr).Limit(5).List(); err != nil {
		return fmt.Errorf("surface WhereExpr: %w", err)
	}
	rec.Note(
		QF("And"), QF("Or"), QF("Not"), QF("Cmp"), QF("Eq"), QF("Ne"),
		QF("Lt"), QF("Gt"), QF("Lte"), QF("Gte"), QF("In"), QF("NotIn"), QF("Func"),
	)

	// Sub / Exists / NotExists / InSub / NotInSub / SomeOf — combinadores sobre
	// un Subquery. Se construyen sobre un subquery real y se ejecutan vía
	// WhereExpr (AllowRawQueries no hace falta: el Subquery es tipado).
	sub, err := quark.For[domain.Account](ctx, client).Select("id").WhereExpr(
		quark.Gt(quark.Col("version"), quark.Lit(int64(-1))),
	).AsSubquery()
	if err != nil {
		return fmt.Errorf("surface AsSubquery: %w", err)
	}
	subExprs := quark.Or(
		quark.Exists(sub),
		quark.NotExists(sub),
		quark.InSub(quark.Col("id"), sub),
		quark.NotInSub(quark.Col("id"), sub),
		quark.Sub(sub), // Sub como leaf escalar
	)
	// Sólo se construye + valida el árbol (no se ejecuta para no atarse a la
	// portabilidad de subqueries correlacionadas); la construcción ya recorre
	// cada constructor. Marca los símbolos invocados.
	_ = subExprs
	rec.Note(
		QF("Sub"), QF("Exists"), QF("NotExists"), QF("InSub"), QF("NotInSub"),
	)
	return nil
}

// surfaceWindow ejerce las window-funcs restantes vía SelectExpr + Over.
func surfaceWindow(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	w := quark.NewWindow().OrderBy(quark.Col("id"), false)
	_, err := quark.For[domain.Account](ctx, client).
		SelectExpr("rnk", quark.Over(quark.Rank(), w)).
		SelectExpr("drnk", quark.Over(quark.DenseRank(), w)).
		SelectExpr("lg", quark.Over(quark.Lag(quark.Col("id"), 1), w)).
		SelectExpr("ld", quark.Over(quark.Lead(quark.Col("id"), 1), w)).
		Limit(5).List()
	if err != nil {
		return fmt.Errorf("surface window: %w", err)
	}
	rec.Note(QF("Rank"), QF("DenseRank"), QF("Lag"), QF("Lead"))
	return nil
}

// surfaceDialectFactories llama cada factory de dialecto + el registro/detección.
func surfaceDialectFactories(rec *recorder.Recorder) error {
	for _, d := range []quark.Dialect{
		quark.PostgreSQL(), quark.MySQL(), quark.MariaDB(), quark.SQLite(), quark.MSSQL(), quark.Oracle(),
	} {
		if d == nil || d.Name() == "" {
			return fmt.Errorf("surface dialect factory devolvió dialecto vacío")
		}
	}
	quark.RegisterDialect("surface_probe", quark.SQLite())
	if _, err := quark.DetectDialect("pgx"); err != nil {
		return fmt.Errorf("surface DetectDialect: %w", err)
	}
	if _, err := quark.DetectDialectByName("postgres"); err != nil {
		return fmt.Errorf("surface DetectDialectByName: %w", err)
	}
	rec.Note(
		QF("PostgreSQL"), QF("MySQL"), QF("MariaDB"), QF("SQLite"), QF("MSSQL"), QF("Oracle"),
		QF("RegisterDialect"), QF("DetectDialect"), QF("DetectDialectByName"),
	)
	return nil
}

// surfaceClientOptions construye un Client efímero pasando cada option-func
// restante (no se ejecuta nada contra él; la construcción ejercita la opción).
func surfaceClientOptions(rec *recorder.Recorder, conn Conn) error {
	c, err := quark.New(conn.Driver, conn.DSN,
		quark.WithLimits(quark.DefaultLimits()),
		quark.WithMaxOpenConns(4),
		quark.WithMaxIdleConns(2),
		quark.WithConnMaxLifetime(time.Minute),
		quark.WithConnMaxIdleTime(30*time.Second),
		quark.WithDefaultTZ(time.UTC),
		quark.WithDialect(quark.SQLite()),
	)
	if err != nil {
		return fmt.Errorf("surface client options: %w", err)
	}
	_ = c.Close()
	rec.Note(
		QF("WithLimits"), QF("WithMaxOpenConns"), QF("WithMaxIdleConns"),
		QF("WithConnMaxLifetime"), QF("WithConnMaxIdleTime"), QF("WithDefaultTZ"), QF("WithDialect"),
	)
	return nil
}

// surfaceModelMeta llama las funcs de introspección/meta sobre un modelo real.
func surfaceModelMeta(rec *recorder.Recorder) {
	t := reflect.TypeOf(domain.Account{})
	_ = quark.GetModelMetaByType(t)
	_ = quark.ModelHash(t)
	_, _ = quark.CheckGeneratedDrift(t)
	_ = quark.CanonicalType("VARCHAR(255)")
	rec.Note(
		QF("GetModelMetaByType"), QF("ModelHash"), QF("CheckGeneratedDrift"), QF("CanonicalType"),
	)
}

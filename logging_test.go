package quark_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// SQLQueryLogger is a QueryObserver that logs SQL statements using slog.
type SQLQueryLogger struct {
	logger *slog.Logger
}

func NewSQLQueryLogger(l *slog.Logger) *SQLQueryLogger {
	if l == nil {
		l = slog.Default()
	}
	return &SQLQueryLogger{logger: l}
}

func (o *SQLQueryLogger) ObserveQuery(e quark.QueryEvent) {
	o.logger.Info("SQL Execution",
		"op", e.Operation,
		"sql", e.SQL,
		"args", e.Args,
		"duration", e.Duration,
		"rows", e.Rows,
		"error", e.Error,
	)
}

func TestSQLLogging(t *testing.T) {
	// 1. Create a logger (here we use a text handler to see it clearly in console)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 2. Inject the logger as a QueryObserver
	sqlLogger := NewSQLQueryLogger(logger)
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	client, _ := quark.New("sqlite", ":memory:", quark.WithLimits(limits), quark.WithQueryObserver(sqlLogger))
	defer client.Close()

	ctx := context.Background()
	type LogUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}

	client.Migrate(ctx, &LogUser{})

	// 3. Perform operations and watch the console
	quark.For[LogUser](ctx, client).Create(&LogUser{Name: "Loggy"})
	quark.For[LogUser](ctx, client).Where("name", "=", "Loggy").List()
}

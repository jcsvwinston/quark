// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f09_codegen is bug-bash phase F9: opt-in codegen (`quark gen`) keeps
// parity with the reflection path.
//
// The committed model/quark_gen.go was emitted by the real `quark gen` binary
// over ./model (the external-consumer flow — the bug-bash module cannot import
// Quark's internal generator). Its init() registers a typed scanner for both
// models and a typed INSERT binder for the integer-PK Account; Doc (string PK)
// registers quark.StubBinder, so its write path falls back to reflection.
//
//   - GeneratedParity: a row written+read through Account (generated scanner +
//     binder) equals the same row through a reflect-only twin — zero drift.
//   - NonIntPKBinderFallsBack: Doc has a generated scanner but no real binder
//     (string PK), so writes use reflection — and still round-trip correctly.
//   - WherePSQLParity: WhereP(AccountColumns.X.Eq(v)) emits SQL byte-identical
//     to Where("x","=",v) (captured via a query observer).
//   - VersionGateFallsBack: generated code registered under a stale contract
//     version is ignored — the runtime uses reflection, no silent error (the
//     fake typed scanner/binder, which panic if called, are never reached).
//   - DriftDetected: CheckGeneratedDrift flags a model whose stored hash no
//     longer matches its shape (changed-but-not-regenerated), and reports the
//     in-sync Account as not drifted.
//   - DryRunWritesNothing: `quark gen --dry-run ./model` prints source to
//     stdout and does not touch the committed quark_gen.go.
//
// SQLite is the cheap proof; the generated scanner routes every field through
// the same quark.ScanTarget the reflection path uses, so parity holds on every
// engine (covered cross-engine by F1).
package f09_codegen

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/phases/f09_codegen/model"
	"github.com/jcsvwinston/quark/bugbash/reporter"

	_ "modernc.org/sqlite"
)

const phase = "f09_codegen"

// reflectAccount mirrors model.Account field-for-field but is a different type
// with no generated code, so every query takes the reflection path.
type reflectAccount struct {
	ID        int64                  `db:"id" pk:"true"`
	Email     string                 `db:"email"`
	Age       int                    `db:"age"`
	Balance   float64                `db:"balance"`
	Active    bool                   `db:"active"`
	Settings  quark.JSON[string]     `db:"settings"`
	Nickname  quark.Nullable[string] `db:"nickname"`
	CreatedAt time.Time              `db:"created_at"`
	UpdatedAt *time.Time             `db:"updated_at"`
}

func (reflectAccount) TableName() string { return "reflect_accounts" }

type rec struct {
	t   *testing.T
	cat reporter.Category
}

func newRec(t *testing.T, cat reporter.Category) rec { return rec{t: t, cat: cat} }

func (r rec) fail(name string, sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase: phase, Test: name, Engine: "sqlite", Category: r.cat, Severity: sev,
		Error: fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestCodegen ./phases/f09_codegen/...",
		},
	})
}

func newClient(t *testing.T, obs ...quark.QueryObserver) *quark.Client {
	t.Helper()
	opts := []any{quark.WithMaxOpenConns(1)}
	for _, o := range obs {
		opts = append(opts, quark.WithQueryObserver(o))
	}
	// Shared-cache in-memory DB unique per test so each starts clean.
	dsn := "file:f09_" + t.Name() + "?mode=memory&cache=shared"
	c, err := quark.New("sqlite", dsn, opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCodegen(t *testing.T) {
	// Guard: the committed quark_gen.go must actually be registered, else the
	// parity tests would silently exercise reflection on both sides.
	if _, has := quark.CheckGeneratedDrift(reflect.TypeOf(model.Account{})); !has {
		t.Fatal("model.Account has no generated code registered — the committed quark_gen.go did not load")
	}

	t.Run("GeneratedParity", generatedParity)
	t.Run("NonIntPKBinderFallsBack", nonIntPKBinderFallsBack)
	t.Run("WherePSQLParity", wherePSQLParity)
	t.Run("VersionGateFallsBack", versionGateFallsBack)
	t.Run("DriftDetected", driftDetected)
	t.Run("DryRunWritesNothing", dryRunWritesNothing)
}

func generatedParity(t *testing.T) {
	r := newRec(t, reporter.CategoryRegression)
	ctx := context.Background()
	c := newClient(t)
	if err := c.Migrate(ctx, &model.Account{}, &reflectAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !quark.GeneratedBinderRegistered(reflect.TypeOf(model.Account{})) {
		r.fail("GeneratedParity", reporter.SeverityP1, "Account has no generated binder — the v3 write path is not exercised")
	}

	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	upd := now.Add(time.Hour)
	g := model.Account{Email: "a@x.com", Age: 41, Balance: 12.5, Active: true,
		Settings: quark.JSON[string]{V: "cfg"}, Nickname: quark.Nullable[string]{V: "nick", Valid: true},
		CreatedAt: now, UpdatedAt: &upd}
	rf := reflectAccount{Email: "a@x.com", Age: 41, Balance: 12.5, Active: true,
		Settings: quark.JSON[string]{V: "cfg"}, Nickname: quark.Nullable[string]{V: "nick", Valid: true},
		CreatedAt: now, UpdatedAt: &upd}

	if err := quark.For[model.Account](ctx, c).Create(&g); err != nil {
		r.fail("GeneratedParity", reporter.SeverityP1, "create generated: %v", err)
		return
	}
	if err := quark.For[reflectAccount](ctx, c).Create(&rf); err != nil {
		r.fail("GeneratedParity", reporter.SeverityP1, "create reflect: %v", err)
		return
	}
	gg, err := quark.For[model.Account](ctx, c).Find(g.ID)
	if err != nil {
		r.fail("GeneratedParity", reporter.SeverityP1, "find generated: %v", err)
		return
	}
	gr, err := quark.For[reflectAccount](ctx, c).Find(rf.ID)
	if err != nil {
		r.fail("GeneratedParity", reporter.SeverityP1, "find reflect: %v", err)
		return
	}
	if gg.Email != gr.Email || gg.Age != gr.Age || gg.Balance != gr.Balance || gg.Active != gr.Active ||
		gg.Settings != gr.Settings || gg.Nickname != gr.Nickname || !gg.CreatedAt.Equal(gr.CreatedAt) {
		r.fail("GeneratedParity", reporter.SeverityP1, "generated vs reflect mismatch: gen=%+v reflect=%+v", gg, gr)
	}
	if (gg.UpdatedAt == nil) != (gr.UpdatedAt == nil) || (gg.UpdatedAt != nil && !gg.UpdatedAt.Equal(*gr.UpdatedAt)) {
		r.fail("GeneratedParity", reporter.SeverityP1, "UpdatedAt mismatch gen=%v reflect=%v", gg.UpdatedAt, gr.UpdatedAt)
	}
}

func nonIntPKBinderFallsBack(t *testing.T) {
	r := newRec(t, reporter.CategoryRegression)
	ctx := context.Background()
	c := newClient(t)
	if err := c.Migrate(ctx, &model.Doc{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Doc has a generated scanner but a StubBinder (string PK) — the write path
	// must fall back to reflection.
	if quark.GeneratedBinderRegistered(reflect.TypeOf(model.Doc{})) {
		r.fail("NonIntPKBinderFallsBack", reporter.SeverityP1, "Doc reports a real generated binder; string-PK models must fall back to reflection")
	}
	if _, has := quark.CheckGeneratedDrift(reflect.TypeOf(model.Doc{})); !has {
		r.fail("NonIntPKBinderFallsBack", reporter.SeverityP1, "Doc has no generated code at all; expected a generated scanner")
	}
	// Reflection write path still round-trips correctly.
	d := model.Doc{Code: "DOC-1", Title: "Title One"}
	if err := quark.For[model.Doc](ctx, c).Create(&d); err != nil {
		r.fail("NonIntPKBinderFallsBack", reporter.SeverityP1, "create Doc (reflect binder): %v", err)
		return
	}
	got, err := quark.For[model.Doc](ctx, c).Find("DOC-1")
	if err != nil {
		r.fail("NonIntPKBinderFallsBack", reporter.SeverityP1, "find Doc: %v", err)
		return
	}
	if got.Code != "DOC-1" || got.Title != "Title One" {
		r.fail("NonIntPKBinderFallsBack", reporter.SeverityP1, "Doc round-trip mismatch: %+v", got)
	}
}

// sqlCapture records the SQL of the last SELECT it observes.
type sqlCapture struct{ last string }

func (s *sqlCapture) ObserveQuery(e quark.QueryEvent) {
	if e.Operation == "SELECT" || e.Operation == "SELECT (stream)" {
		s.last = e.SQL
	}
}

func wherePSQLParity(t *testing.T) {
	r := newRec(t, reporter.CategoryRegression)
	ctx := context.Background()
	cap := &sqlCapture{}
	c := newClient(t, cap)
	if err := c.Migrate(ctx, &model.Account{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := quark.For[model.Account](ctx, c).WhereP(model.AccountColumns.Email.Eq("a@x.com")).List(); err != nil {
		r.fail("WherePSQLParity", reporter.SeverityP1, "WhereP list: %v", err)
		return
	}
	sqlP := cap.last
	if _, err := quark.For[model.Account](ctx, c).Where("email", "=", "a@x.com").List(); err != nil {
		r.fail("WherePSQLParity", reporter.SeverityP1, "Where list: %v", err)
		return
	}
	sqlW := cap.last
	if sqlP == "" || sqlW == "" {
		r.fail("WherePSQLParity", reporter.SeverityP1, "observer captured no SQL (P=%q W=%q)", sqlP, sqlW)
		return
	}
	if sqlP != sqlW {
		r.fail("WherePSQLParity", reporter.SeverityP1, "WhereP and Where produced different SQL:\n  WhereP: %s\n  Where:  %s", sqlP, sqlW)
	}
}

func versionGateFallsBack(t *testing.T) {
	r := newRec(t, reporter.CategoryRegression)
	ctx := context.Background()
	c := newClient(t)
	if err := c.Migrate(ctx, &staleModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tp := reflect.TypeOf(staleModel{})
	// Register generated code under a STALE contract version. The fakes flag if
	// invoked (instead of panicking — staleGenCalled is asserted false below), so
	// no panic is left in the process-global registry. staleModel is private to
	// this test package and used only here, so the registration is inert
	// elsewhere; -count=N just overwrites it idempotently.
	staleGenCalled.Store(false)
	quark.RegisterTypedScanner(tp, func(*sql.Rows, any) error { staleGenCalled.Store(true); return nil })
	quark.RegisterTypedBinder(tp, func(any, quark.BindMode) ([]string, []any, error) { staleGenCalled.Store(true); return nil, nil, nil })
	quark.RegisterGeneratedMeta(tp, quark.GeneratedMeta{ContractVersion: quark.GenContractVersion - 1, ModelHash: quark.ModelHash(tp)})

	if quark.GeneratedBinderRegistered(tp) {
		r.fail("VersionGateFallsBack", reporter.SeverityP1, "stale-version binder reported as registered; version gate did not reject it")
	}
	// Reflection must serve the write+read without reaching the stale fakes.
	s := staleModel{Name: "ok"}
	if err := quark.For[staleModel](ctx, c).Create(&s); err != nil {
		r.fail("VersionGateFallsBack", reporter.SeverityP1, "create on stale-version model (should use reflection): %v", err)
		return
	}
	got, err := quark.For[staleModel](ctx, c).Find(s.ID)
	if err != nil || got.Name != "ok" {
		r.fail("VersionGateFallsBack", reporter.SeverityP1, "reflection fallback round-trip failed: got=%+v err=%v", got, err)
	}
	if staleGenCalled.Load() {
		r.fail("VersionGateFallsBack", reporter.SeverityP1, "stale-version generated code was invoked; the version gate did not route to reflection")
	}
}

func driftDetected(t *testing.T) {
	r := newRec(t, reporter.CategoryRegression)

	// In-sync model: no drift.
	if drifted, has := quark.CheckGeneratedDrift(reflect.TypeOf(model.Account{})); !has || drifted {
		r.fail("DriftDetected", reporter.SeverityP1, "Account drift=%v has=%v, want has=true drift=false (in sync)", drifted, has)
	}

	// Register generated meta with the CURRENT version but a WRONG hash — the
	// shape "changed" without regeneration.
	tp := reflect.TypeOf(driftModel{})
	quark.RegisterTypedScanner(tp, func(*sql.Rows, any) error { return nil })
	quark.RegisterGeneratedMeta(tp, quark.GeneratedMeta{ContractVersion: quark.GenContractVersion, ModelHash: "stale-hash-does-not-match"})
	if drifted, has := quark.CheckGeneratedDrift(tp); !has || !drifted {
		r.fail("DriftDetected", reporter.SeverityP1, "driftModel drift=%v has=%v, want has=true drift=true", drifted, has)
	}
}

func dryRunWritesNothing(t *testing.T) {
	r := newRec(t, reporter.CategoryRegression)
	repoRoot, bugbashDir := repoPaths(t)

	genPath := filepath.Join(bugbashDir, "phases", "f09_codegen", "model", "quark_gen.go")
	before, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatalf("read committed quark_gen.go: %v", err)
	}

	// Build the quark binary in the ROOT module (its go.sum has cobra/viper/…),
	// then run it from the bug-bash dir so the ./phases/... model pattern resolves
	// in the quarkbugbash module. (go run from the bug-bash dir would resolve
	// cmd/quark's deps against the bug-bash go.sum and fail.)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "quarkgen")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/quark")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		r.fail("DryRunWritesNothing", reporter.SeverityP2, "build quark binary: %v\n%s", err, out)
		return
	}
	cmd := exec.CommandContext(ctx, bin, "gen", "--dry-run", "./phases/f09_codegen/model/")
	cmd.Dir = bugbashDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.fail("DryRunWritesNothing", reporter.SeverityP2, "quark gen --dry-run failed: %v\n%s", err, out)
		return
	}
	if len(out) == 0 {
		r.fail("DryRunWritesNothing", reporter.SeverityP1, "--dry-run produced no stdout")
	}
	after, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatalf("re-read quark_gen.go: %v", err)
	}
	if string(before) != string(after) {
		r.fail("DryRunWritesNothing", reporter.SeverityP1, "--dry-run modified the committed quark_gen.go")
	}
}

// repoPaths returns (repoRoot, bugbashModuleDir). It derives them from the test
// working directory — `go test` runs with CWD = the package (phase) dir, a real
// runtime path, so this is robust under -trimpath (unlike runtime.Caller, whose
// embedded path is trimmed).
func repoPaths(t *testing.T) (string, string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// wd = <root>/bugbash/phases/f09_codegen
	bugbashDir := filepath.Join(wd, "..", "..")
	repoRoot := filepath.Join(bugbashDir, "..")
	return repoRoot, bugbashDir
}

// staleGenCalled is set by the stale-version fakes in VersionGateFallsBack; it
// must stay false (the version gate routes to reflection instead).
var staleGenCalled atomic.Bool

type staleModel struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (staleModel) TableName() string { return "f09_stale_models" }

type driftModel struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (driftModel) TableName() string { return "f09_drift_models" }

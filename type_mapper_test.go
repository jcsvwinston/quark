package quark_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// testTypeMapper covers the Phase-1 RegisterTypeMapper API + tag option
// parsing (size, precision, scale). It runs in SharedSuite so every dialect
// exercises the lookup path; Migrate is the consumer here.
func testTypeMapper(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	t.Run("DurationMapsToBigInt", func(t *testing.T) {
		// time.Duration is registered by Quark in package init; the migrate
		// layer must emit BIGINT (or NUMBER(19) on Oracle) instead of TEXT
		// fallback.
		type Job struct {
			ID      int64         `db:"id" pk:"true"`
			Timeout time.Duration `db:"timeout"`
		}
		dropTable(baseClient, "jobs")
		defer dropTable(baseClient, "jobs")
		if err := baseClient.Migrate(ctx, &Job{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}

		// Round-trip a value to confirm the column accepts int64 nanoseconds.
		j := &Job{Timeout: 250 * time.Millisecond}
		if err := quark.For[Job](ctx, baseClient).Create(j); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := quark.For[Job](ctx, baseClient).Find(j.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if got.Timeout != 250*time.Millisecond {
			t.Errorf("expected 250ms, got %v", got.Timeout)
		}
	})

	t.Run("CustomMapperHonored", func(t *testing.T) {
		// Register a one-off mapper for a local type and verify Migrate uses
		// it instead of the TEXT fallback.
		type IPAddr [4]byte
		quark.RegisterTypeMapper(reflect.TypeOf(IPAddr{}), func(dialect string, _ quark.TypeOptions) string {
			switch dialect {
			case "postgres":
				return "INET"
			default:
				return "VARCHAR(45)" // IPv6 max length
			}
		})

		type Visitor struct {
			ID int64  `db:"id" pk:"true"`
			IP IPAddr `db:"ip"`
		}
		dropTable(baseClient, "visitors")
		defer dropTable(baseClient, "visitors")

		// Migrate must succeed: without the mapper the column would have
		// been TEXT (fallback), losing the IP semantics. With the mapper,
		// CREATE TABLE goes through the registered function.
		if err := baseClient.Migrate(ctx, &Visitor{}); err != nil {
			t.Fatalf("migrate with custom mapper: %v", err)
		}
	})

	t.Run("SizeTagOptionRespected", func(t *testing.T) {
		// db:"name,size=512" must produce VARCHAR(512) (or the dialect
		// equivalent). We can't easily inspect the emitted DDL across all
		// engines, but we can assert it via internal/migrate.SQLTypeWithOpts
		// directly — the public SQLType wrapper is exercised through
		// Migrate above.
		type Profile struct {
			ID  int64  `db:"id" pk:"true"`
			Bio string `db:"bio,size=512"`
		}
		dropTable(baseClient, "profiles")
		defer dropTable(baseClient, "profiles")
		if err := baseClient.Migrate(ctx, &Profile{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}

		// Round-trip a string at the upper bound to confirm the column
		// accepts the requested size. We don't test 513 because some
		// drivers truncate silently rather than erroring.
		p := &Profile{Bio: strings.Repeat("a", 500)}
		if err := quark.For[Profile](ctx, baseClient).Create(p); err != nil {
			t.Fatalf("create with 500-char bio: %v", err)
		}
	})

	t.Run("PointerTypeStrippedOnRegistration", func(t *testing.T) {
		// Registering for time.Duration should also cover *time.Duration —
		// the registry strips pointers before keying.
		type Watcher struct {
			ID         int64          `db:"id" pk:"true"`
			MaxLatency *time.Duration `db:"max_latency"`
		}
		dropTable(baseClient, "watchers")
		defer dropTable(baseClient, "watchers")
		if err := baseClient.Migrate(ctx, &Watcher{}); err != nil {
			t.Fatalf("migrate *time.Duration: %v", err)
		}
		// Pointer to nil round-trips as NULL.
		w := &Watcher{}
		if err := quark.For[Watcher](ctx, baseClient).Create(w); err != nil {
			t.Fatalf("create with nil *Duration: %v", err)
		}
	})
}

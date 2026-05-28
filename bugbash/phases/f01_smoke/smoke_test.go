// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f01_smoke is bug-bash phase F1: per-engine smoke. It round-trips
// every rich column type and exercises the CRUD primitives against the
// real domain on each selected engine. Failures are aggregated via
// reporter.Fail, not aborted on first.
package f01_smoke

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/domain"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f01_smoke"

var engineFlag = flag.String("engines", "sqlite",
	"comma-separated engines (sqlite,postgres,mysql,mariadb,mssql,oracle) or 'all'")

func selectedEngines() []string {
	v := strings.TrimSpace(*engineFlag)
	if v == "" || v == "all" {
		return tools.AllEngines
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// record files a structured failure for the current engine without
// aborting the phase.
func record(t *testing.T, eng, test string, cat reporter.Category, sev reporter.Severity, format string, args ...any) {
	t.Helper()
	reporter.Fail(t, reporter.Failure{
		Phase:    phase,
		Test:     test,
		Engine:   eng,
		Category: cat,
		Severity: sev,
		Error:    fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestSmoke ./phases/f01_smoke/... -engines=" + eng,
		},
	})
}

func TestSmoke(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var containerEngines []string
		for _, e := range engines {
			if e != tools.SQLite {
				containerEngines = append(containerEngines, e)
			}
		}
		tools.Down(containerEngines...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) {
			client, err := quark.New(conn.Driver, conn.DSN)
			if err != nil {
				t.Fatalf("quark.New(%q): %v", conn.Driver, err) // blocking: cannot test this engine
			}
			t.Cleanup(func() {
				_ = client.Close()
				if eng == tools.SQLite {
					_ = os.Remove(conn.DSN)
				}
			})
			if err := client.Migrate(ctx, domain.AllModels()...); err != nil {
				t.Fatalf("migrate domain on %s: %v", eng, err) // blocking
			}

			org := seedOrg(t, ctx, client, eng)
			t.Run("RoundTrip", func(t *testing.T) { roundTrip(t, ctx, client, eng, org) })
			t.Run("CRUD", func(t *testing.T) { crud(t, ctx, client, eng) })
		})
	}
}

// seedOrg creates the parent organization the round-trip rows reference,
// and exercises uuid + JSON[struct] round-trip in the process. Returns the
// created org (with backfilled ID) for FK reuse.
func seedOrg(t *testing.T, ctx context.Context, c *quark.Client, eng string) *domain.Organization {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	id := uuid.New()
	org := &domain.Organization{
		UUID: id,
		Name: "Acme " + eng,
		Slug: "acme-" + eng,
		Plan: "enterprise",
		Settings: quark.JSON[domain.OrgSettings]{V: domain.OrgSettings{
			DefaultLocale:  "es-ES",
			EnabledModules: []string{"sales", "inventory"},
			SLAHours:       24,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := quark.For[domain.Organization](ctx, c).Create(org); err != nil {
		t.Fatalf("seed organization on %s: %v", eng, err) // blocking: round-trip needs the FK
	}
	if org.ID == 0 {
		record(t, eng, "RoundTrip/Organization.PKBackfill", reporter.CategoryDialectSpecific, reporter.SeverityP1,
			"Create did not backfill the autoincrement PK (ID still 0)")
	}
	got, err := quark.For[domain.Organization](ctx, c).Find(org.ID)
	if err != nil {
		record(t, eng, "RoundTrip/Organization.Find", reporter.CategoryDialectSpecific, reporter.SeverityP1, "find: %v", err)
		// Return the partially-verified org anyway: its ID was backfilled by
		// Create, so roundTrip can still use it as a FK parent. Its UUID/
		// Settings were not confirmed here — a downstream FK failure would be
		// a follow-on symptom, not a new bug.
		return org
	}
	if got.UUID != id {
		record(t, eng, "RoundTrip/uuid", reporter.CategoryDialectSpecific, reporter.SeverityP1,
			"uuid round-trip diff: got %v want %v", got.UUID, id)
	}
	if !reflect.DeepEqual(got.Settings.V, org.Settings.V) {
		record(t, eng, "RoundTrip/JSON[struct]", reporter.CategoryDialectSpecific, reporter.SeverityP1,
			"JSON[OrgSettings] round-trip diff: got %+v want %+v", got.Settings.V, org.Settings.V)
	}
	return org
}

func roundTrip(t *testing.T, ctx context.Context, c *quark.Client, eng string, org *domain.Organization) {
	now := time.Now().UTC().Truncate(time.Second)

	// --- decimal (default precision + explicit precision/scale), Array[T], JSON[map] ---
	price := decimal.RequireFromString("19.99")
	weight := decimal.RequireFromString("1.234")
	prod := &domain.Product{
		OrganizationID: org.ID,
		CategoryID:     1,
		SKU:            "SKU-" + eng,
		Name:           "Widget",
		Price:          price,
		Currency:       "EUR",
		Weight:         weight,
		Active:         true,
		Tags:           quark.Array[string]{V: []string{"new", "featured"}},
		Attrs: quark.JSON[domain.ProductAttrs]{V: domain.ProductAttrs{
			Color: "red", Size: "M",
			Dimensions: map[string]any{"w": 10.0, "h": 20.0},
		}},
		CreatedAt: now,
	}
	if err := quark.For[domain.Product](ctx, c).Create(prod); err != nil {
		record(t, eng, "RoundTrip/Product.Create", reporter.CategoryDialectSpecific, reporter.SeverityP1, "create: %v", err)
	} else if got, err := quark.For[domain.Product](ctx, c).Find(prod.ID); err != nil {
		record(t, eng, "RoundTrip/Product.Find", reporter.CategoryDialectSpecific, reporter.SeverityP1, "find: %v", err)
	} else {
		if !got.Price.Equal(price) {
			record(t, eng, "RoundTrip/decimal", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"decimal Price diff: got %s want %s", got.Price, price)
		}
		if !got.Weight.Equal(weight) {
			record(t, eng, "RoundTrip/decimal(precision,scale)", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"decimal Weight diff: got %s want %s", got.Weight, weight)
		}
		if !reflect.DeepEqual(got.Tags.V, prod.Tags.V) {
			record(t, eng, "RoundTrip/Array", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"Array[string] diff: got %v want %v", got.Tags.V, prod.Tags.V)
		}
		if !reflect.DeepEqual(got.Attrs.V, prod.Attrs.V) {
			record(t, eng, "RoundTrip/JSON[map]", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"JSON[ProductAttrs] diff: got %+v want %+v", got.Attrs.V, prod.Attrs.V)
		}
	}

	// --- []byte, Nullable[string] (set), per-column TZ ---
	hash := []byte{0x01, 0x02, 0x03, 0xff}
	login := now
	u := &domain.User{
		OrganizationID: org.ID,
		Email:          "alice@" + eng + ".test",
		PasswordHash:   hash,
		DisplayName:    quark.SomeOf("Alice"),
		Locale:         "es",
		LastLoginAt:    &login,
		CreatedAt:      now,
	}
	if err := quark.For[domain.User](ctx, c).Create(u); err != nil {
		record(t, eng, "RoundTrip/User.Create", reporter.CategoryDialectSpecific, reporter.SeverityP1, "create: %v", err)
	} else if got, err := quark.For[domain.User](ctx, c).Find(u.ID); err != nil {
		record(t, eng, "RoundTrip/User.Find", reporter.CategoryDialectSpecific, reporter.SeverityP1, "find: %v", err)
	} else {
		if !bytes.Equal(got.PasswordHash, hash) {
			record(t, eng, "RoundTrip/[]byte", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"BLOB diff: got %x want %x", got.PasswordHash, hash)
		}
		if !got.DisplayName.Valid || got.DisplayName.V != "Alice" {
			record(t, eng, "RoundTrip/Nullable(set)", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"Nullable[string] set diff: got valid=%v v=%q", got.DisplayName.Valid, got.DisplayName.V)
		}
		if got.LastLoginAt == nil || !got.LastLoginAt.Equal(login) {
			record(t, eng, "RoundTrip/time(TZ)", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"per-column TZ timestamp diff: got %v want %v", got.LastLoginAt, login)
		}
	}

	// --- Nullable[string] (NULL), time.Duration, decimals summing ---
	o := &domain.Order{
		OrganizationID: org.ID,
		CustomerID:     1,
		Number:         "ORD-" + eng,
		Status:         "paid",
		Subtotal:       decimal.RequireFromString("100.00"),
		Tax:            decimal.RequireFromString("21.00"),
		Total:          decimal.RequireFromString("121.00"),
		Currency:       "EUR",
		SLADuration:    48 * time.Hour,
		PlacedAt:       now,
		Notes:          quark.NullOf[string](),
		CreatedAt:      now,
	}
	if err := quark.For[domain.Order](ctx, c).Create(o); err != nil {
		record(t, eng, "RoundTrip/Order.Create", reporter.CategoryDialectSpecific, reporter.SeverityP1, "create: %v", err)
	} else if got, err := quark.For[domain.Order](ctx, c).Find(o.ID); err != nil {
		record(t, eng, "RoundTrip/Order.Find", reporter.CategoryDialectSpecific, reporter.SeverityP1, "find: %v", err)
	} else {
		if got.SLADuration != 48*time.Hour {
			record(t, eng, "RoundTrip/Duration", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"time.Duration diff: got %v want %v", got.SLADuration, 48*time.Hour)
		}
		if got.Notes.Valid {
			record(t, eng, "RoundTrip/Nullable(NULL)", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"Nullable[string] NULL came back Valid=true (v=%q)", got.Notes.V)
		}
		if !got.Total.Equal(o.Total) {
			record(t, eng, "RoundTrip/decimal(total)", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"decimal Total diff: got %s want %s", got.Total, o.Total)
		}
	}
}

// crud exercises the CRUD primitives on Organization (which has a
// soft-delete column), independent of the round-trip rows.
func crud(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	now := time.Now().UTC().Truncate(time.Second)
	org := &domain.Organization{
		Name: "CRUD " + eng, Slug: "crud-" + eng, Plan: "free",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := quark.For[domain.Organization](ctx, c).Create(org); err != nil {
		record(t, eng, "CRUD/Create", reporter.CategoryDialectSpecific, reporter.SeverityP1, "create: %v", err)
		return
	}

	if n, err := quark.For[domain.Organization](ctx, c).Count(); err != nil {
		record(t, eng, "CRUD/Count", reporter.CategoryDialectSpecific, reporter.SeverityP1, "count: %v", err)
	} else if n < 1 {
		record(t, eng, "CRUD/Count", reporter.CategoryDialectSpecific, reporter.SeverityP1, "count returned %d, want >=1", n)
	}

	// Update: change Plan, expect 1 row affected and the change to persist.
	org.Plan = "pro"
	if n, err := quark.For[domain.Organization](ctx, c).Update(org); err != nil {
		record(t, eng, "CRUD/Update", reporter.CategoryDialectSpecific, reporter.SeverityP1, "update: %v", err)
	} else if n != 1 {
		record(t, eng, "CRUD/Update", reporter.CategoryDialectSpecific, reporter.SeverityP2, "update affected %d rows, want 1", n)
	}

	// UpdateFields: change only Name; Plan in memory is dirtied but must NOT persist.
	org.Name = "CRUD2 " + eng
	org.Plan = "should-not-persist"
	if _, err := quark.For[domain.Organization](ctx, c).UpdateFields(org, "name"); err != nil {
		record(t, eng, "CRUD/UpdateFields", reporter.CategoryDialectSpecific, reporter.SeverityP1, "update_fields: %v", err)
	} else if got, err := quark.For[domain.Organization](ctx, c).Find(org.ID); err != nil {
		record(t, eng, "CRUD/UpdateFields.Find", reporter.CategoryDialectSpecific, reporter.SeverityP1, "find: %v", err)
	} else {
		if got.Name != "CRUD2 "+eng {
			record(t, eng, "CRUD/UpdateFields", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"UpdateFields did not persist name: got %q", got.Name)
		}
		if got.Plan != "pro" {
			record(t, eng, "CRUD/UpdateFields", reporter.CategoryDialectSpecific, reporter.SeverityP1,
				"UpdateFields leaked an unlisted field: Plan=%q want pro", got.Plan)
		}
	}

	// List + Where.
	if list, err := quark.For[domain.Organization](ctx, c).Where("slug", "=", "crud-"+eng).List(); err != nil {
		record(t, eng, "CRUD/List", reporter.CategoryDialectSpecific, reporter.SeverityP1, "list: %v", err)
	} else if len(list) != 1 {
		record(t, eng, "CRUD/List", reporter.CategoryDialectSpecific, reporter.SeverityP1, "list by slug returned %d, want 1", len(list))
	}

	// Soft delete: row excluded from default queries afterward.
	if _, err := quark.For[domain.Organization](ctx, c).Delete(org); err != nil {
		record(t, eng, "CRUD/Delete", reporter.CategoryDialectSpecific, reporter.SeverityP1, "soft delete: %v", err)
	} else if _, err := quark.For[domain.Organization](ctx, c).Find(org.ID); err == nil {
		record(t, eng, "CRUD/Delete", reporter.CategoryDialectSpecific, reporter.SeverityP1,
			"soft-deleted row still visible to Find by default")
	}

	// HardDelete: a fresh row, removed for good.
	tmp := &domain.Organization{Name: "TMP " + eng, Slug: "tmp-" + eng, Plan: "free", CreatedAt: now, UpdatedAt: now}
	if err := quark.For[domain.Organization](ctx, c).Create(tmp); err != nil {
		record(t, eng, "CRUD/HardDelete.seed", reporter.CategoryDialectSpecific, reporter.SeverityP2, "create tmp: %v", err)
	} else if n, err := quark.For[domain.Organization](ctx, c).HardDelete(tmp); err != nil {
		record(t, eng, "CRUD/HardDelete", reporter.CategoryDialectSpecific, reporter.SeverityP1, "hard delete: %v", err)
	} else if n != 1 {
		record(t, eng, "CRUD/HardDelete", reporter.CategoryDialectSpecific, reporter.SeverityP2, "hard delete affected %d rows, want 1", n)
	}
}

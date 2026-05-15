package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jcsvwinston/quark"
)

// Product represents a multi-tenant product model using RLS
type Product struct {
	ID        int64     `db:"id" pk:"true"`
	TenantID  string    `db:"tenant_id"` // Used for RLS
	Name      string    `db:"name"`
	Price     float64   `db:"price"`
	CreatedAt time.Time `db:"created_at"`
}

func main() {
	ctx := context.Background()

	// 1. Initialize Postgres connection
	// Set QUARK_EXAMPLE_POSTGRES_DSN="postgres://user:pass@localhost:5432/db?sslmode=disable"
	dsn := os.Getenv("QUARK_EXAMPLE_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://quark_user:quark_pass@localhost:5432/quark_test?sslmode=disable"
	}

	// 2. Initialize Base Quark Client (sql.Open is handled internally)
	baseClient, err := quark.New("pgx", dsn,
		quark.WithMaxOpenConns(25),
		quark.WithMaxIdleConns(5),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer baseClient.Close()

	// 3. Initialize TenantRouter for client-side row-level scoping.
	// (The engine-enforced RowLevelSecurityNative variant is delivered
	// in Fase 5 F5-2; this example uses the client-side strategy that
	// works across all six dialects.)
	router := quark.NewTenantRouter(
		quark.TenantConfig{
			Strategy:     quark.RowLevelSecurityClient,
			BaseClient:   baseClient,
			TenantColumn: "tenant_id",
		},
		func(ctx context.Context) string {
			if val, ok := ctx.Value("tenant_id").(string); ok {
				return val
			}
			return ""
		},
		nil, // No factory needed for RLS
	)

	// 4. Migrate (using base client)
	fmt.Println("🚀 Migrating Postgres schema...")
	if err := baseClient.Migrate(ctx, &Product{}); err != nil {
		log.Fatal(err)
	}

	// 5. Create products for different tenants
	fmt.Println("📝 Creating multi-tenant products...")

	// Create for Tenant A
	ctxA := context.WithValue(ctx, "tenant_id", "tenant-a")
	prodA := &Product{Name: "Laptop", Price: 1200.0}
	if err := quark.For[Product](ctxA, router).Create(prodA); err != nil {
		log.Fatal(err)
	}

	// Create for Tenant B
	ctxB := context.WithValue(ctx, "tenant_id", "tenant-b")
	prodB := &Product{Name: "Smartphone", Price: 800.0}
	if err := quark.For[Product](ctxB, router).Create(prodB); err != nil {
		log.Fatal(err)
	}

	// 6. Verify Isolation
	fmt.Println("🔍 Verifying Tenant Isolation...")

	itemsA, _ := quark.For[Product](ctxA, router).List()
	if len(itemsA) > 0 {
		fmt.Printf("Tenant A sees %d products: %v\n", len(itemsA), itemsA[0].Name)
	}

	itemsB, _ := quark.For[Product](ctxB, router).List()
	if len(itemsB) > 0 {
		fmt.Printf("Tenant B sees %d products: %v\n", len(itemsB), itemsB[0].Name)
	}
}

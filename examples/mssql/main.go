package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jcsvwinston/quark"
	_ "github.com/microsoft/go-mssqldb"
)

// Product represents a product model
type Product struct {
	ID        int64     `db:"id" pk:"true"`
	Name      string    `db:"name"`
	Price     float64   `db:"price"`
	CreatedAt time.Time `db:"created_at"`
}

func main() {
	ctx := context.Background()

	// 1. Initialize MSSQL connection
	dsn := os.Getenv("QUARK_EXAMPLE_MSSQL_DSN")
	if dsn == "" {
		dsn = "sqlserver://sa:QuarkTest123!@localhost:1433?database=master"
	}

	// 2. Initialize Quark Client (sql.Open is handled internally)
	client, err := quark.New("sqlserver", dsn,
		quark.WithMaxOpenConns(25),
		quark.WithMaxIdleConns(5),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	// 3. Auto-Migrate
	fmt.Println("🚀 Migrating schema...")
	if err := client.Migrate(ctx, &Product{}); err != nil {
		log.Fatal(err)
	}

	// 4. Create a Product
	newProduct := &Product{
		Name:      "Quark Framework",
		Price:     99.99,
		CreatedAt: time.Now(),
	}
	fmt.Println("📝 Creating product...")
	if err := quark.For[Product](ctx, client).Create(newProduct); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✅ Product created with ID: %d\n", newProduct.ID)

	// 5. Query with Builder
	fmt.Println("🔍 Querying products...")
	products, err := quark.For[Product](ctx, client).
		Where("price", ">=", 50).
		OrderBy("created_at", "DESC").
		Limit(10).
		List()

	if err != nil {
		log.Fatal(err)
	}

	for _, p := range products {
		fmt.Printf("- %s, Price: $%.2f\n", p.Name, p.Price)
	}
}

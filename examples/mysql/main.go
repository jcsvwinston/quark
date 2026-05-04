package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jcsvwinston/quark"
	_ "github.com/go-sql-driver/mysql"
)

// Order represents a complex order model
type Order struct {
	ID        int64     `db:"id" pk:"true"`
	OrderNo   string    `db:"order_no"`
	Amount    float64   `db:"amount"`
	Status    string    `db:"status"`
	CreatedAt time.Time `db:"created_at"`
}

func main() {
	ctx := context.Background()

	// 1. Initialize MySQL connection
	dsn := os.Getenv("QUARK_EXAMPLE_MYSQL_DSN")
	if dsn == "" {
		dsn = "quark_user:quark_pass@tcp(localhost:3306)/quark_test?parseTime=true"
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 2. Initialize Quark
	client, err := quark.New(db, quark.WithDialect(quark.MySQL()))
	if err != nil {
		log.Fatal(err)
	}

	// 3. Migrate
	fmt.Println("🚀 Migrating MySQL schema...")
	if err := client.Migrate(ctx, &Order{}); err != nil {
		log.Fatal(err)
	}

	// 4. Transaction Example
	fmt.Println("💸 Executing transactional order...")
	err = client.Tx(ctx, func(tx *quark.Tx) error {
		order := &Order{
			OrderNo:   "ORD-1001",
			Amount:    250.75,
			Status:    "PENDING",
			CreatedAt: time.Now(),
		}

		if err := quark.ForTx[Order](ctx, tx).Create(order); err != nil {
			return err
		}

		fmt.Printf("✅ Order %s saved within transaction (ID: %d)\n", order.OrderNo, order.ID)
		return nil // Commit
	})

	if err != nil {
		log.Fatalf("Transaction failed: %v", err)
	}

	// 5. Streaming Results
	fmt.Println("🌊 Streaming orders...")
	err = quark.For[Order](ctx, client).Iter(func(o Order) error {
		fmt.Printf("- Order: %s, Status: %s\n", o.OrderNo, o.Status)
		return nil
	})

	if err != nil {
		log.Fatal(err)
	}
}

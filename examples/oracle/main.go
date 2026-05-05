package main

import (
	"fmt"
	"os"
	"time"
	// "context"
	// "database/sql"
	// "log"
	// "github.com/jcsvwinston/quark"
)

// Department represents a department model
type Department struct {
	ID        int64     `db:"id" pk:"true"`
	Name      string    `db:"name"`
	Location  string    `db:"location"`
	CreatedAt time.Time `db:"created_at"`
}

func main() {
	// 1. Initialize Oracle connection
	dsn := os.Getenv("QUARK_EXAMPLE_ORACLE_DSN")
	if dsn == "" {
		dsn = "sys/QuarkTest123!@localhost:1521/XEPDB1?as=sysdba"
	}

	// Warning: Godror requires CGO, so we will only demonstrate the initialization logic here
	// and comment out the sql.Open to avoid compilation errors if godror is not available.

	fmt.Println("⚠️ Note: The godror Oracle driver requires CGO to be enabled.")
	fmt.Printf("Connecting with DSN: %s\n", dsn)

	// Uncomment the following lines when CGO is enabled and Godror is available
	/*
		db, err := sql.Open("godror", dsn)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()

		// 2. Initialize Quark Client
		client, err := quark.New(db, quark.WithDialect(quark.Oracle()))
		if err != nil {
			log.Fatal(err)
		}

		// 3. Auto-Migrate
		fmt.Println("🚀 Migrating schema...")
		if err := client.Migrate(ctx, &Department{}); err != nil {
			log.Fatal(err)
		}

		// 4. Create a Department
		newDept := &Department{
			Name:      "Engineering",
			Location:  "Building A",
			CreatedAt: time.Now(),
		}
		fmt.Println("📝 Creating department...")
		if err := quark.For[Department](ctx, client).Create(newDept); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("✅ Department created with ID: %d\n", newDept.ID)

		// 5. Query with Builder
		fmt.Println("🔍 Querying departments...")
		departments, err := quark.For[Department](ctx, client).
			Where("location", "=", "Building A").
			OrderBy("created_at", "DESC").
			Limit(10).
			List()

		if err != nil {
			log.Fatal(err)
		}

		for _, d := range departments {
			fmt.Printf("- %s, Location: %s\n", d.Name, d.Location)
		}
	*/
}

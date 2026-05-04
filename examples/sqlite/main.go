package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

// User represents a real-world user model
type User struct {
	ID        int64      `db:"id" pk:"true"`
	Username  string     `db:"username" validate:"required,min=3"`
	Email     string     `db:"email" validate:"required,email"`
	Age       int        `db:"age"`
	CreatedAt time.Time  `db:"created_at"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func main() {
	ctx := context.Background()

	// 1. Initialize SQLite connection
	db, err := sql.Open("sqlite", "example.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 2. Initialize Quark Client
	client, err := quark.New(db, quark.WithDialect(quark.SQLite()))
	if err != nil {
		log.Fatal(err)
	}

	// 3. Auto-Migrate
	fmt.Println("🚀 Migrating schema...")
	if err := client.Migrate(ctx, &User{}); err != nil {
		log.Fatal(err)
	}

	// 4. Create a User
	newUser := &User{
		Username:  "jdoe",
		Email:     "john@example.com",
		Age:       30,
		CreatedAt: time.Now(),
	}
	fmt.Println("📝 Creating user...")
	if err := quark.For[User](ctx, client).Create(newUser); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("✅ User created with ID: %d\n", newUser.ID)

	// 5. Query with Builder
	fmt.Println("🔍 Querying users...")
	users, err := quark.For[User](ctx, client).
		Where("age", ">=", 18).
		OrderBy("created_at", "DESC").
		Limit(10).
		List()

	if err != nil {
		log.Fatal(err)
	}

	for _, u := range users {
		fmt.Printf("- %s (%s), Age: %d\n", u.Username, u.Email, u.Age)
	}

	// 6. Pagination Example
	fmt.Println("📑 Pagination example...")
	page, err := quark.For[User](ctx, client).Paginate(10, 0) // Page 0, 10 per page
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Total Records: %d, Total Pages: %d\n", page.Total, page.TotalPages)
}

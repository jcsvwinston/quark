// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package render

import "time"

// The example models the playground renders against. They are the shape of
// the query bench's fixtures (see docs/query-bench.md) without the table
// prefix and without relations: a small shop — users who place orders of
// products in categories — which is enough to show a filter, a join, a
// group and a subquery.

// User has a deleted_at column, so Quark adds the soft-delete predicate to
// every read unless the query says WithTrashed().
type User struct {
	ID        int64      `db:"id" pk:"true"`
	Email     string     `db:"email"`
	Country   string     `db:"country"`
	Profile   string     `db:"profile"`
	CreatedAt time.Time  `db:"created_at"`
	DeletedAt *time.Time `db:"deleted_at"`
}

type Order struct {
	ID       int64     `db:"id" pk:"true"`
	UserID   int64     `db:"user_id"`
	Total    float64   `db:"total"`
	Status   string    `db:"status"`
	PlacedAt time.Time `db:"placed_at"`
}

type Product struct {
	ID         int64   `db:"id" pk:"true"`
	CategoryID int64   `db:"category_id"`
	Name       string  `db:"name"`
	Price      float64 `db:"price"`
	Stock      int     `db:"stock"`
	Attrs      string  `db:"attrs"`
	Active     bool    `db:"active"`
}

type Category struct {
	ID       int64  `db:"id" pk:"true"`
	Name     string `db:"name"`
	ParentID *int64 `db:"parent_id"`
}

type OrderItem struct {
	ID        int64   `db:"id" pk:"true"`
	OrderID   int64   `db:"order_id"`
	ProductID int64   `db:"product_id"`
	Qty       int     `db:"qty"`
	UnitPrice float64 `db:"unit_price"`
}

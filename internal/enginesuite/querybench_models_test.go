// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import "time"

// The query bench (see docs/query-bench.md) measures which of 60 real-world
// queries Quark can express with its typed API and which still need
// RawQuery. It is the numerator of the A4 gate, so it lives in the engine
// suite: the same cases can be pointed at any of the six engines, and a case
// that only passes on SQLite is a portability finding, not a pass.
//
// Table names are prefixed qb_ so the bench cannot collide with the other
// suites sharing this package.

type qbCategory struct {
	ID       int64  `db:"id" pk:"true"`
	Name     string `db:"name"`
	ParentID *int64 `db:"parent_id"`
}

type qbProduct struct {
	ID         int64   `db:"id" pk:"true"`
	CategoryID int64   `db:"category_id"`
	Name       string  `db:"name"`
	Price      float64 `db:"price"`
	Stock      int     `db:"stock"`
	Attrs      string  `db:"attrs"`
	Active     bool    `db:"active"`
}

type qbUser struct {
	ID        int64      `db:"id" pk:"true"`
	Email     string     `db:"email"`
	Country   string     `db:"country"`
	Profile   string     `db:"profile"`
	CreatedAt time.Time  `db:"created_at"`
	DeletedAt *time.Time `db:"deleted_at"`
	Orders    []qbOrder  `rel:"has_many" join:"user_id"`
}

type qbOrder struct {
	ID       int64     `db:"id" pk:"true"`
	UserID   int64     `db:"user_id"`
	Total    float64   `db:"total"`
	Status   string    `db:"status"`
	PlacedAt time.Time `db:"placed_at"`
}

type qbOrderItem struct {
	ID        int64   `db:"id" pk:"true"`
	OrderID   int64   `db:"order_id"`
	ProductID int64   `db:"product_id"`
	Qty       int     `db:"qty"`
	UnitPrice float64 `db:"unit_price"`
}

// qbOrderEmail is a projection target: a DTO with no table of its own. Q17
// asks whether a join can be projected onto it.
type qbOrderEmail struct {
	OrderID int64  `db:"order_id"`
	Email   string `db:"email"`
}

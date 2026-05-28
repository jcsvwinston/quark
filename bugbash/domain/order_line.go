// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import "github.com/shopspring/decimal"

// OrderLine is the highest-volume table (5M rows in F4). Exercises IN
// chunking on eager load and optimistic editing of quantity post-checkout.
type OrderLine struct {
	ID        int64           `db:"id" pk:"true"`
	OrderID   int64           `db:"order_id"`
	ProductID int64           `db:"product_id"`
	Quantity  int             `db:"quantity"`
	UnitPrice decimal.Decimal `db:"unit_price"`
	Discount  decimal.Decimal `db:"discount"`
	LineTotal decimal.Decimal `db:"line_total"`
	Sequence  int             `db:"sequence"`

	Order   *Order   `rel:"belongs_to" join:"order_id"`
	Product *Product `rel:"belongs_to" join:"product_id"`
}

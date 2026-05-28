// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// Inventory is the per-(product, warehouse) stock row. Exercises optimistic
// locking (version) and FOR UPDATE / SKIP LOCKED reservation scenarios.
type Inventory struct {
	ID          int64     `db:"id" pk:"true"`
	ProductID   int64     `db:"product_id"`
	WarehouseID int64     `db:"warehouse_id"`
	Quantity    int       `db:"quantity"`
	Reserved    int       `db:"reserved"`
	Version     int       `db:"version" quark:"version"`
	UpdatedAt   time.Time `db:"updated_at" quark:"tz=UTC"`

	Product   *Product   `rel:"belongs_to" join:"product_id"`
	Warehouse *Warehouse `rel:"belongs_to" join:"warehouse_id"`
}

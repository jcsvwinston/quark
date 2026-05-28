// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/jcsvwinston/quark"
)

// Product exercises decimal with explicit precision/scale, JSON[T] holding
// a map[string]any, soft delete, and a has_many to Inventory.
type Product struct {
	ID             int64                    `db:"id" pk:"true"`
	OrganizationID int64                    `db:"organization_id"`
	CategoryID     int64                    `db:"category_id"`
	SKU            string                   `db:"sku" quark:"unique"`
	Name           string                   `db:"name"`
	Description    string                   `db:"description"`
	Price          decimal.Decimal          `db:"price"`
	Currency       string                   `db:"currency"`
	Weight         decimal.Decimal          `db:"weight,precision=10,scale=3"`
	Active         bool                     `db:"active"`
	Tags           quark.Array[string]      `db:"tags"`
	Attrs          quark.JSON[ProductAttrs] `db:"attrs"`
	CreatedAt      time.Time                `db:"created_at" quark:"tz=UTC"`
	DeletedAt      *time.Time               `db:"deleted_at" quark:"tz=UTC"`

	Category  *Category   `rel:"belongs_to" join:"category_id"`
	Inventory []Inventory `rel:"has_many" join:"product_id"`
}

type ProductAttrs struct {
	Color      string         `json:"color"`
	Size       string         `json:"size"`
	Dimensions map[string]any `json:"dimensions"`
}

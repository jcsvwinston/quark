// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/jcsvwinston/quark"
)

// Order exercises time.Duration (pre-wired mapper), nullable timestamps with
// a TZ tag, summable decimals, and deep nested preload
// (Order.Lines.Product.Category).
type Order struct {
	ID             int64                  `db:"id" pk:"true"`
	OrganizationID int64                  `db:"organization_id"`
	CustomerID     int64                  `db:"customer_id"`
	Number         string                 `db:"number" quark:"unique"`
	Status         string                 `db:"status"` // pending|paid|shipped|cancelled
	Subtotal       decimal.Decimal        `db:"subtotal"`
	Tax            decimal.Decimal        `db:"tax"`
	Total          decimal.Decimal        `db:"total"`
	Currency       string                 `db:"currency"`
	SLADuration    time.Duration          `db:"sla_duration"`
	PlacedAt       time.Time              `db:"placed_at" quark:"tz=UTC"`
	ShippedAt      *time.Time             `db:"shipped_at" quark:"tz=UTC"`
	Notes          quark.Nullable[string] `db:"notes"`
	CreatedAt      time.Time              `db:"created_at" quark:"tz=UTC"`
	DeletedAt      *time.Time             `db:"deleted_at" quark:"tz=UTC"`

	Customer *Customer   `rel:"belongs_to" join:"customer_id"`
	Lines    []OrderLine `rel:"has_many" join:"order_id"`
	Payments []Payment   `rel:"has_many" join:"order_id"`
	// AuditEvents is the polymorphic side of AuditEvent (subject_type +
	// subject_id). Quark models polymorphism as an owner-side has-many
	// filtered by a type literal (rel:"polymorphic"), not as a belongs-to
	// union — so the DOMAIN.md "AuditEvent.Subject" sketch is wired here as
	// its inverse. F3 exercises it: loading Order.AuditEvents must return
	// only rows whose subject_type = "Order".
	AuditEvents []AuditEvent `rel:"polymorphic" polymorphic:"subject_type:Order" join:"subject_id"`
}

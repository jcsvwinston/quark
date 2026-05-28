// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/jcsvwinston/quark"
)

// Invoice exercises two timezones in the same table (Europe/Madrid issue/due
// vs UTC elsewhere in the domain), an Array of external tax-rule codes, a
// nullable PDF BLOB, and an optional belongs_to (standalone invoices).
type Invoice struct {
	ID             int64                  `db:"id" pk:"true"`
	OrganizationID int64                  `db:"organization_id"`
	OrderID        *int64                 `db:"order_id"` // nullable: standalone invoices
	Number         string                 `db:"number" quark:"unique"`
	IssuedAt       time.Time              `db:"issued_at" quark:"tz=Europe/Madrid"`
	DueAt          time.Time              `db:"due_at" quark:"tz=Europe/Madrid"`
	Status         string                 `db:"status"`
	Subtotal       decimal.Decimal        `db:"subtotal"`
	TaxRules       quark.Array[string]    `db:"tax_rules"`
	Total          decimal.Decimal        `db:"total"`
	PDF            quark.Nullable[[]byte] `db:"pdf"`

	Lines []InvoiceLine `rel:"has_many" join:"invoice_id"`
	Order *Order        `rel:"belongs_to" join:"order_id"`
}

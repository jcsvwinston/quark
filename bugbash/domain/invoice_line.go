// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import "github.com/shopspring/decimal"

// InvoiceLine carries decimal quantity/rate/total for an Invoice.
type InvoiceLine struct {
	ID          int64           `db:"id" pk:"true"`
	InvoiceID   int64           `db:"invoice_id"`
	Description string          `db:"description"`
	Quantity    decimal.Decimal `db:"quantity"`
	UnitPrice   decimal.Decimal `db:"unit_price"`
	TaxRate     decimal.Decimal `db:"tax_rate"`
	Total       decimal.Decimal `db:"total"`
}

// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// TaxRule exercises date-range queries (valid_from/valid_until with a
// nullable end) and composite indexes.
type TaxRule struct {
	ID             int64           `db:"id" pk:"true"`
	OrganizationID int64           `db:"organization_id"`
	Code           string          `db:"code" quark:"unique"`
	Name           string          `db:"name"`
	Rate           decimal.Decimal `db:"rate"`
	Country        string          `db:"country"`
	CategoryID     *int64          `db:"category_id"` // nullable
	ValidFrom      time.Time       `db:"valid_from" quark:"tz=UTC"`
	ValidUntil     *time.Time      `db:"valid_until" quark:"tz=UTC"`
}

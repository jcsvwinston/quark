// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Refund belongs to a Payment.
type Refund struct {
	ID          int64           `db:"id" pk:"true"`
	PaymentID   int64           `db:"payment_id"`
	Amount      decimal.Decimal `db:"amount"`
	Reason      string          `db:"reason"`
	ProcessedAt time.Time       `db:"processed_at" quark:"tz=UTC"`

	Payment *Payment `rel:"belongs_to" join:"payment_id"`
}

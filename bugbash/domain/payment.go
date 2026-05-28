// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/jcsvwinston/quark"
)

// Payment exercises transactional hooks (BeforeUpdate writes an audit row;
// AfterUpdate publishes to the EventBus post-commit) and raw JSON metadata.
type Payment struct {
	ID          int64                      `db:"id" pk:"true"`
	OrderID     int64                      `db:"order_id"`
	Method      string                     `db:"method"` // card|transfer|cash
	Amount      decimal.Decimal            `db:"amount"`
	Currency    string                     `db:"currency"`
	Reference   string                     `db:"reference"`
	Provider    string                     `db:"provider"`
	Status      string                     `db:"status"`
	ProcessedAt time.Time                  `db:"processed_at" quark:"tz=UTC"`
	Metadata    quark.JSON[map[string]any] `db:"metadata"`

	Refunds []Refund `rel:"has_many" join:"payment_id"`
}

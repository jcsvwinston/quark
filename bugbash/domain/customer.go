// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/jcsvwinston/quark"
)

// Customer exercises a typed-struct JSON[Address] alongside a raw
// JSON[map[string]any], plus UUID and decimal.
type Customer struct {
	ID             int64                      `db:"id" pk:"true"`
	OrganizationID int64                      `db:"organization_id"`
	UUID           uuid.UUID                  `db:"uuid"`
	Name           string                     `db:"name"`
	Email          quark.Nullable[string]     `db:"email"`
	Phone          quark.Nullable[string]     `db:"phone"`
	Address        quark.JSON[Address]        `db:"address"`
	CreditLimit    decimal.Decimal            `db:"credit_limit"`
	TaxID          string                     `db:"tax_id"`
	Metadata       quark.JSON[map[string]any] `db:"metadata"`
	CreatedAt      time.Time                  `db:"created_at" quark:"tz=UTC"`
	DeletedAt      *time.Time                 `db:"deleted_at" quark:"tz=UTC"`

	Orders []Order `rel:"has_many" join:"customer_id"`
}

type Address struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

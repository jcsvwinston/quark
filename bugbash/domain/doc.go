// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package domain is the ERP-SaaS multi-tenant model set the bug-bash uses
// to exercise Quark across all six engines. It is the executable
// translation of bugbash/DOMAIN.md — see that file for the rationale
// behind each entity and the F4 cardinalities.
//
// The structs use Quark's real tag grammar (rel:/join:/m2m: for
// relations, pk:/quark:/db: for columns), not the pre-implementation
// sketch in DOMAIN.md. Custom column types (uuid.UUID, decimal.Decimal)
// are wired through RegisterTypeMapper in mappers.go.
package domain

// AllModels returns one pointer per domain table, ordered so that a model
// defining an explicit join table (UserRole) is migrated before the model
// that declares the many-to-many relation over it (User). f00_install
// migrates this whole set against each engine.
func AllModels() []any {
	return []any{
		&Organization{},
		&Role{}, &UserRole{},
		&User{}, &UserProfile{},
		&Category{},
		&Product{},
		&Warehouse{}, &Inventory{},
		&Customer{},
		&Order{}, &OrderLine{},
		&Payment{}, &Refund{},
		&Invoice{}, &InvoiceLine{},
		&TaxRule{},
		&AuditEvent{},
		&Attachment{},
		&Note{},
	}
}

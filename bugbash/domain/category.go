// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// Category is self-referential (parent_id), exercising recursive CTEs and
// nested has_many preload (Children.Children.Children).
type Category struct {
	ID             int64     `db:"id" pk:"true"`
	OrganizationID int64     `db:"organization_id"`
	ParentID       *int64    `db:"parent_id"` // nullable for roots
	Name           string    `db:"name"`
	Slug           string    `db:"slug"`
	Depth          int       `db:"depth"`
	CreatedAt      time.Time `db:"created_at" quark:"tz=UTC"`

	Parent   *Category  `rel:"belongs_to" join:"parent_id"`
	Children []Category `rel:"has_many" join:"parent_id"`
}

// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/jcsvwinston/quark"
)

// Organization is the tenant root. Exercises JSON[T], UUID (via mapper),
// soft delete, and unique constraints.
type Organization struct {
	ID        int64                   `db:"id" pk:"true"`
	UUID      uuid.UUID               `db:"uuid"`
	Name      string                  `db:"name" quark:"not_null,unique"`
	Slug      string                  `db:"slug" quark:"unique"`
	Plan      string                  `db:"plan"` // free | pro | enterprise
	Settings  quark.JSON[OrgSettings] `db:"settings"`
	CreatedAt time.Time               `db:"created_at" quark:"tz=UTC"`
	UpdatedAt time.Time               `db:"updated_at" quark:"tz=UTC"`
	DeletedAt *time.Time              `db:"deleted_at" quark:"tz=UTC"`
}

type OrgSettings struct {
	DefaultLocale  string   `json:"default_locale"`
	EnabledModules []string `json:"enabled_modules"`
	SLAHours       int      `json:"sla_hours"`
}

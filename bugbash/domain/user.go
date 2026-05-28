// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/jcsvwinston/quark"
)

// User exercises belongs_to, has_one, many_to_many, Nullable[string],
// optimistic locking (version), a per-column non-UTC timezone, and a BLOB
// password hash.
type User struct {
	ID             int64                  `db:"id" pk:"true"`
	OrganizationID int64                  `db:"organization_id"`
	Email          string                 `db:"email" quark:"unique"`
	PasswordHash   []byte                 `db:"password_hash"`
	DisplayName    quark.Nullable[string] `db:"display_name"`
	Locale         string                 `db:"locale"`
	Version        int                    `db:"version" quark:"version"`
	LastLoginAt    *time.Time             `db:"last_login_at" quark:"tz=Europe/Madrid"`
	CreatedAt      time.Time              `db:"created_at" quark:"tz=UTC"`
	DeletedAt      *time.Time             `db:"deleted_at" quark:"tz=UTC"`

	Organization *Organization `rel:"belongs_to" join:"organization_id"`
	Profile      *UserProfile  `rel:"has_one" join:"user_id"`
	Roles        []Role        `rel:"many_to_many" m2m:"user_roles:user_id:role_id"`
}

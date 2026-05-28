// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// UserRole is the explicit join table for User<->Role with extra metadata
// (GrantedAt) and a composite primary key. It is migrated before User so
// the table carrying GrantedAt exists before the many_to_many relation on
// User would otherwise auto-create a bare join table.
type UserRole struct {
	UserID    int64     `db:"user_id" pk:"true"`
	RoleID    int64     `db:"role_id" pk:"true"`
	GrantedAt time.Time `db:"granted_at" quark:"tz=UTC"`
}

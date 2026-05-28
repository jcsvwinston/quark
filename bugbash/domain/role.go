// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/jcsvwinston/quark"
)

// Role is the many side of User<->Role. Exercises Array[T] for permissions.
type Role struct {
	ID             int64               `db:"id" pk:"true"`
	OrganizationID int64               `db:"organization_id"`
	Name           string              `db:"name"`
	Permissions    quark.Array[string] `db:"permissions"`
	CreatedAt      time.Time           `db:"created_at" quark:"tz=UTC"`
}

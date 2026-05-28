// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// Warehouse holds geo coordinates as plain float64 columns.
type Warehouse struct {
	ID             int64     `db:"id" pk:"true"`
	OrganizationID int64     `db:"organization_id"`
	Code           string    `db:"code" quark:"unique"`
	Location       string    `db:"location"`
	GeoLat         float64   `db:"geo_lat"`
	GeoLng         float64   `db:"geo_lng"`
	CreatedAt      time.Time `db:"created_at" quark:"tz=UTC"`
}

// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// Note is a polymorphic large-TEXT row (5-100 KB body), used to exercise
// LIKE scans per engine. Polymorphic relation wiring is deferred to F3.
type Note struct {
	ID          int64     `db:"id" pk:"true"`
	OrgID       int64     `db:"organization_id"`
	SubjectType string    `db:"subject_type"`
	SubjectID   int64     `db:"subject_id"`
	AuthorID    int64     `db:"author_id"`
	Body        string    `db:"body"`
	Pinned      bool      `db:"pinned"`
	CreatedAt   time.Time `db:"created_at" quark:"tz=UTC"`
}

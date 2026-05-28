// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// Attachment is a polymorphic large-BLOB row (1-10 MB content), used to
// exercise streaming reads via Iter/Cursor. Polymorphic relation wiring is
// deferred to F3; F0 stores the subject as plain columns.
type Attachment struct {
	ID          int64     `db:"id" pk:"true"`
	OrgID       int64     `db:"organization_id"`
	SubjectType string    `db:"subject_type"`
	SubjectID   int64     `db:"subject_id"`
	Filename    string    `db:"filename"`
	MimeType    string    `db:"mime_type"`
	SizeBytes   int64     `db:"size_bytes"`
	Content     []byte    `db:"content"`
	UploadedAt  time.Time `db:"uploaded_at" quark:"tz=UTC"`
}

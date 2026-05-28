// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/jcsvwinston/quark"
)

// AuditEvent is a polymorphic log row (Subject = type + id). High volume
// (every CRUD emits one). The polymorphic relation itself is wired and
// exercised in F3; F0 only needs the table to migrate, so the subject is
// stored as plain columns here.
type AuditEvent struct {
	ID          int64                      `db:"id" pk:"true"`
	OrgID       int64                      `db:"organization_id"`
	ActorID     *int64                     `db:"actor_id"` // nullable: system events
	SubjectType string                     `db:"subject_type"`
	SubjectID   int64                      `db:"subject_id"`
	Action      string                     `db:"action"` // created|updated|deleted|refunded
	Diff        quark.JSON[map[string]any] `db:"diff"`
	Metadata    quark.JSON[map[string]any] `db:"metadata"`
	OccurredAt  time.Time                  `db:"occurred_at" quark:"tz=UTC"`
}

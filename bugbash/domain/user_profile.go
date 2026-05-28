// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/jcsvwinston/quark"
)

// UserProfile is 1:1 with User. Exercises Array[T], nested JSON[T], and a
// nullable BLOB.
type UserProfile struct {
	ID        int64                    `db:"id" pk:"true"`
	UserID    int64                    `db:"user_id" quark:"unique"`
	Bio       string                   `db:"bio"`
	Avatar    quark.Nullable[[]byte]   `db:"avatar"`
	Prefs     quark.JSON[ProfilePrefs] `db:"prefs"`
	Tags      quark.Array[string]      `db:"tags"`
	CreatedAt time.Time                `db:"created_at" quark:"tz=UTC"`
}

type ProfilePrefs struct {
	Theme         string `json:"theme"`
	Notifications struct {
		Email bool `json:"email"`
		Push  bool `json:"push"`
	} `json:"notifications"`
}

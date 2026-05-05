package main

import "time"

// Author represents a blog author.
type Author struct {
	ID        int64     `db:"id"         pk:"true"      json:"id"`
	Name      string    `db:"name"       quark:"not_null" json:"name"`
	Email     string    `db:"email"      quark:"unique"  json:"email"`
	Bio       string    `db:"bio"                        json:"bio"`
	CreatedAt time.Time `db:"created_at"                 json:"created_at"`
}

// Post represents a blog post.
// The DeletedAt field enables soft-delete behaviour automatically.
type Post struct {
	ID          int64      `db:"id"           pk:"true"       json:"id"`
	Title       string     `db:"title"        quark:"not_null" json:"title"`
	Body        string     `db:"body"                          json:"body"`
	AuthorID    int64      `db:"author_id"                     json:"author_id"`
	Published   bool       `db:"published"                     json:"published"`
	PublishedAt *time.Time `db:"published_at"                  json:"published_at,omitempty"`
	CreatedAt   time.Time  `db:"created_at"                    json:"created_at"`
	DeletedAt   *time.Time `db:"deleted_at"                    json:"deleted_at,omitempty"`
}

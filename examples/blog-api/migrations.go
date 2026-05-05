package main

import (
	"context"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
)

func registerMigrations() {
	migrate.Register(&migrate.Migration{
		ID:   "20260501_create_authors",
		Name: "create authors table",
		Up: func(ctx context.Context, client *quark.Client) error {
			return client.Migrate(ctx, &Author{})
		},
		Down: func(ctx context.Context, client *quark.Client) error {
			return client.Exec(ctx, `DROP TABLE IF EXISTS authors`)
		},
	})

	migrate.Register(&migrate.Migration{
		ID:   "20260502_create_posts",
		Name: "create posts table",
		Up: func(ctx context.Context, client *quark.Client) error {
			return client.Migrate(ctx, &Post{})
		},
		Down: func(ctx context.Context, client *quark.Client) error {
			return client.Exec(ctx, `DROP TABLE IF EXISTS posts`)
		},
	})
}

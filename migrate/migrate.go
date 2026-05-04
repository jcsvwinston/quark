package migrate

import (
	"context"
	"fmt"
	"sort"

	"github.com/jcsvwinston/quark"
)

type Migration struct {
	ID      string
	Name    string
	Message string
	Up      func(ctx context.Context, client *quark.Client) error
	Down    func(ctx context.Context, client *quark.Client) error
}

var registry = make(map[string]*Migration)

func Register(m *Migration) {
	registry[m.ID] = m
}

// Reset clears all registered migrations. Intended for use in tests only.
func Reset() {
	registry = make(map[string]*Migration)
}

type Migrator struct {
	client    *quark.Client
	tableName string
}

func NewMigrator(client *quark.Client) *Migrator {
	return &Migrator{
		client:    client,
		tableName: "quark_migrations",
	}
}

func (m *Migrator) Init(ctx context.Context) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id VARCHAR(255) PRIMARY KEY,
			name VARCHAR(255),
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`, m.tableName)
	return m.client.Exec(ctx, query)
}

func (m *Migrator) GetApplied(ctx context.Context) (map[string]bool, error) {
	// Use raw DB to bypass SQLGuard validation for internal queries
	rows, err := m.client.Raw().QueryContext(ctx, fmt.Sprintf("SELECT id FROM %s", m.tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		applied[id] = true
	}
	return applied, nil
}

func (m *Migrator) Up(ctx context.Context, steps int) error {
	if err := m.Init(ctx); err != nil {
		return err
	}

	applied, err := m.GetApplied(ctx)
	if err != nil {
		return err
	}

	var ids []string
	for id := range registry {
		if !applied[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	count := 0
	for _, id := range ids {
		if steps > 0 && count >= steps {
			break
		}

		migration := registry[id]
		fmt.Printf("Applying migration: %s (%s)...\n", id, migration.Name)
		if err := migration.Up(ctx, m.client); err != nil {
			return fmt.Errorf("failed to apply migration %s: %w", id, err)
		}

		insertSQL := fmt.Sprintf("INSERT INTO %s (id, name) VALUES (%s, %s)",
			m.tableName,
			m.client.Dialect().Placeholder(1),
			m.client.Dialect().Placeholder(2),
		)
		if err := m.client.Exec(ctx, insertSQL, id, migration.Name); err != nil {
			return err
		}
		count++
	}

	if count == 0 {
		fmt.Println("No pending migrations.")
	} else {
		fmt.Printf("Applied %d migrations.\n", count)
	}

	return nil
}

// UpDryRun previews which migrations would be applied without executing them.
func (m *Migrator) UpDryRun(ctx context.Context, steps int) error {
	if err := m.Init(ctx); err != nil {
		return err
	}

	applied, err := m.GetApplied(ctx)
	if err != nil {
		return err
	}

	var ids []string
	for id := range registry {
		if !applied[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	if len(ids) == 0 {
		fmt.Println("No pending migrations.")
		return nil
	}

	fmt.Println("[dry-run] Pending migrations (not applied):")
	count := 0
	for _, id := range ids {
		if steps > 0 && count >= steps {
			break
		}
		migration := registry[id]
		fmt.Printf("  [pending] %s — %s\n", id, migration.Name)
		count++
	}
	fmt.Printf("\n%d migration(s) would be applied.\n", count)
	return nil
}

func (m *Migrator) Down(ctx context.Context, steps int) error {
	if err := m.Init(ctx); err != nil {
		return err
	}

	// Use raw DB to bypass SQLGuard validation for internal queries
	rows, err := m.client.Raw().QueryContext(ctx, fmt.Sprintf("SELECT id FROM %s ORDER BY id DESC", m.tableName))
	if err != nil {
		return err
	}
	defer rows.Close()

	var applied []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		applied = append(applied, id)
	}

	count := 0
	for _, id := range applied {
		if steps > 0 && count >= steps {
			break
		}

		migration, ok := registry[id]
		if !ok {
			return fmt.Errorf("migration %s applied but not found in registry", id)
		}

		fmt.Printf("Reverting migration: %s (%s)...\n", id, migration.Name)
		if err := migration.Down(ctx, m.client); err != nil {
			return fmt.Errorf("failed to revert migration %s: %w", id, err)
		}

		deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE id = %s",
			m.tableName,
			m.client.Dialect().Placeholder(1),
		)
		if err := m.client.Exec(ctx, deleteSQL, id); err != nil {
			return err
		}
		count++
	}

	if count == 0 {
		fmt.Println("No migrations to revert.")
	} else {
		fmt.Printf("Reverted %d migrations.\n", count)
	}

	return nil
}

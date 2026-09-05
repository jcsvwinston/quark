package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
)

type Migration struct {
	ID      string
	Name    string
	Message string
	Up      func(ctx context.Context, client *quark.Client) error
	Down    func(ctx context.Context, client *quark.Client) error

	// UpTx and DownTx are the transactional forms: when set, and when the
	// dialect can roll DDL back (PostgreSQL, SQLite, SQL Server), the
	// migrator runs them and the ledger row in ONE transaction, so a
	// migration that fails halfway leaves neither schema nor ledger
	// behind. On MySQL, MariaDB and Oracle — where DDL commits itself —
	// they run on a transaction all the same, but only the ledger row is
	// atomic with the last statement; the migrator says so at Debug. A
	// migration that sets both forms is used through the transactional
	// one where it applies and Up/Down elsewhere (QK-6).
	UpTx   func(ctx context.Context, tx *sql.Tx) error
	DownTx func(ctx context.Context, tx *sql.Tx) error
}

var registry = make(map[string]*Migration)

func Register(m *Migration) {
	registry[m.ID] = m
}

// RegisteredCount reports how many migrations are registered in this binary.
// The CLI uses it to refuse a no-op `migrate up`/`down`: a standalone binary
// that never imported the project's migrations package has an empty registry,
// and "No pending migrations" there would be a lie.
func RegisteredCount() int {
	return len(registry)
}

// RegisteredIDs returns the IDs of every migration registered in this binary,
// sorted ascending (the order Up applies them). The CLI's `migrate status`
// uses it to compute the pending set.
func RegisteredIDs() []string {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Reset clears all registered migrations. Intended for use in tests only.
func Reset() {
	registry = make(map[string]*Migration)
}

type Migrator struct {
	client      *quark.Client
	tableName   string
	lock        bool
	lockName    string
	lockTimeout time.Duration
	logger      *slog.Logger
}

// MigratorOption configures NewMigrator.
type MigratorOption func(*Migrator)

// WithoutLock runs Up and Down without the cluster-wide migration lock.
// The default takes it (QK-6): two replicas running `migrate up` at once
// used to both apply the same pending migration.
func WithoutLock() MigratorOption {
	return func(m *Migrator) { m.lock = false }
}

// WithLockTimeout bounds how long Up and Down wait for the migration lock
// held by another process; the default is 30s.
func WithLockTimeout(d time.Duration) MigratorOption {
	return func(m *Migrator) {
		if d > 0 {
			m.lockTimeout = d
		}
	}
}

// WithLockName sets the advisory lock's name; the default is
// "quark:schema", shared by every migrator of an application.
func WithLockName(name string) MigratorOption {
	return func(m *Migrator) {
		if strings.TrimSpace(name) != "" {
			m.lockName = name
		}
	}
}

// WithLogger routes the migrator's progress lines to logger; the default is
// the client's logger. A library writing to stdout with fmt.Printf was the
// third thing QK-6 named.
func WithLogger(logger *slog.Logger) MigratorOption {
	return func(m *Migrator) {
		if logger != nil {
			m.logger = logger
		}
	}
}

func NewMigrator(client *quark.Client, opts ...MigratorOption) *Migrator {
	m := &Migrator{
		client:      client,
		tableName:   "quark_migrations",
		lock:        true,
		lockName:    "quark:schema",
		lockTimeout: 30 * time.Second,
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.logger == nil {
		m.logger = client.Logger()
	}
	return m
}

// acquireLock takes the dialect's cluster-wide migration lock and returns
// its release. A dialect without one (SQLite: single writer) is a no-op
// noted at Debug; any other failure is the caller's error — a lock held by
// a stuck peer past the timeout is a reason to stop, not to proceed.
func (m *Migrator) acquireLock(ctx context.Context) (func(), error) {
	if !m.lock {
		return func() {}, nil
	}
	lock, err := m.client.AcquireMigrationLock(ctx, m.lockName, m.lockTimeout)
	if err != nil {
		if errors.Is(err, quark.ErrUnsupportedFeature) {
			m.logger.Debug("migrate: no distributed lock on this dialect; proceeding without one", "dialect", m.client.Dialect().Name())
			return func() {}, nil
		}
		return nil, fmt.Errorf("migrate: acquiring lock %q: %w", m.lockName, err)
	}
	return func() {
		if err := lock.Release(context.Background()); err != nil {
			m.logger.Warn("migrate: releasing lock", "lock", m.lockName, "error", err)
		}
	}, nil
}

// applyUp runs one migration and records it. With UpTx and a dialect that
// rolls DDL back, migration and ledger row share a transaction.
func (m *Migrator) applyUp(ctx context.Context, id string, migration *Migration) error {
	insertSQL := fmt.Sprintf("INSERT INTO %s (id, name) VALUES (%s, %s)",
		m.tableName,
		m.client.Dialect().Placeholder(1),
		m.client.Dialect().Placeholder(2),
	)
	if migration.UpTx != nil {
		if !m.client.Dialect().SupportsTransactionalDDL() {
			m.logger.Debug("migrate: DDL commits itself on this dialect; only the ledger row is atomic with the last statement", "id", id, "dialect", m.client.Dialect().Name())
		}
		tx, err := m.client.Raw().BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin migration %s: %w", id, err)
		}
		if err := migration.UpTx(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to apply migration %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, insertSQL, id, migration.Name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration %s: %w", id, err)
		}
		return tx.Commit()
	}
	if migration.Up == nil {
		return fmt.Errorf("migration %s has neither Up nor UpTx", id)
	}
	if err := migration.Up(ctx, m.client); err != nil {
		return fmt.Errorf("failed to apply migration %s: %w", id, err)
	}
	return m.client.Exec(ctx, insertSQL, id, migration.Name)
}

// revertDown is applyUp's mirror for Down/DownTx.
func (m *Migrator) revertDown(ctx context.Context, id string, migration *Migration) error {
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE id = %s",
		m.tableName,
		m.client.Dialect().Placeholder(1),
	)
	if migration.DownTx != nil {
		tx, err := m.client.Raw().BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin revert of %s: %w", id, err)
		}
		if err := migration.DownTx(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to revert migration %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, deleteSQL, id); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to unrecord migration %s: %w", id, err)
		}
		return tx.Commit()
	}
	if migration.Down == nil {
		return fmt.Errorf("migration %s has neither Down nor DownTx", id)
	}
	if err := migration.Down(ctx, m.client); err != nil {
		return fmt.Errorf("failed to revert migration %s: %w", id, err)
	}
	return m.client.Exec(ctx, deleteSQL, id)
}

func (m *Migrator) Init(ctx context.Context) error {
	// The bookkeeping table DDL is dialect-specific: SQL Server has no
	// CREATE TABLE IF NOT EXISTS (and TIMESTAMP there means rowversion, not a
	// datetime), and Oracle has neither IF NOT EXISTS nor that TIMESTAMP default
	// spelling. Same per-dialect shape as the backfill state table. Run via Raw
	// (like GetApplied) so the SQL Server existence guard isn't rejected by the
	// raw-query validator.
	name := m.client.Dialect().Quote(m.tableName)
	var ddl string
	switch m.client.Dialect().Name() {
	case "mssql":
		// The sys.tables.name comparison uses the bare table name (a string
		// literal), not the quoted identifier — sys.tables stores names without
		// the delimiters Quote() would add. tableName is the hardcoded
		// "quark_migrations", so there is no injection surface here.
		ddl = fmt.Sprintf(`IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = '%s')
			CREATE TABLE %s (
				id NVARCHAR(255) NOT NULL PRIMARY KEY,
				name NVARCHAR(255),
				applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`, m.tableName, name)
	case "oracle":
		ddl = fmt.Sprintf(`CREATE TABLE %s (
			id VARCHAR2(255) NOT NULL,
			name VARCHAR2(255),
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
			CONSTRAINT pk_%s PRIMARY KEY (id)
		)`, name, m.tableName)
	default: // postgres, mysql, mariadb, sqlite
		ddl = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id VARCHAR(255) PRIMARY KEY,
			name VARCHAR(255),
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`, name)
	}
	if _, err := m.client.Raw().ExecContext(ctx, ddl); err != nil {
		// Oracle has no IF NOT EXISTS; ORA-00955 (name already used) means the
		// table is already there, which is the idempotent success case.
		if m.client.Dialect().Name() == "oracle" && strings.Contains(err.Error(), "ORA-00955") {
			return nil
		}
		return err
	}
	return nil
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
	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer release()

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
		m.logger.Info("migrate: applying", "id", id, "name", migration.Name)
		if err := m.applyUp(ctx, id, migration); err != nil {
			return err
		}
		count++
	}

	if count == 0 {
		m.logger.Info("migrate: no pending migrations")
	} else {
		m.logger.Info("migrate: applied", "count", count)
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
	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer release()

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

		m.logger.Info("migrate: reverting", "id", id, "name", migration.Name)
		if err := m.revertDown(ctx, id, migration); err != nil {
			return err
		}
		count++
	}

	if count == 0 {
		m.logger.Info("migrate: no migrations to revert")
	} else {
		m.logger.Info("migrate: reverted", "count", count)
	}

	return nil
}

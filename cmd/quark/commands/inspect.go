package commands

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fatih/color"
	cli_db "github.com/jcsvwinston/quark/cmd/quark/internal/db"
	internaldb "github.com/jcsvwinston/quark/internal/db"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	inspectFormat string
	inspectModel  string
)

func init() {
	inspectCmd.AddCommand(inspectSchemaCmd)
	inspectCmd.AddCommand(inspectTableCmd)
	inspectCmd.AddCommand(inspectSQLCmd)

	inspectCmd.PersistentFlags().StringVar(&inspectFormat, "format", "table", "Output format (table|json|yaml)")
	inspectSQLCmd.Flags().StringVar(&inspectModel, "model", "", "Table name to introspect")

	rootCmd.AddCommand(inspectCmd)
}

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Database introspection tools",
}

// Like migrate, the inspect subcommands feed scripts and CI checks, so
// failures must exit non-zero (RunE → main.go prints and exits 1).
var inspectSchemaCmd = &cobra.Command{
	Use:           "schema",
	Short:         "Show full database schema",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInspectSchema()
	},
}

var inspectTableCmd = &cobra.Command{
	Use:           "table <name>",
	Short:         "Show structure of a specific table",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInspectTable(args[0])
	},
}

var inspectSQLCmd = &cobra.Command{
	Use:   "sql",
	Short: "Reconstruct CREATE TABLE DDL from a live table",
	Long: `Introspects a table in the connected database and prints an equivalent
CREATE TABLE statement reconstructed from its live columns. It does not
generate SQL from Go model structs — for that, see client.Sync or 'quark gen'.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInspectSQL()
	},
}

// tableReport is the shape rendered by --format json|yaml.
type tableReport struct {
	Table   string         `json:"table" yaml:"table"`
	Columns []columnReport `json:"columns" yaml:"columns"`
}

type columnReport struct {
	Name     string  `json:"name" yaml:"name"`
	Type     string  `json:"type" yaml:"type"`
	Nullable bool    `json:"nullable" yaml:"nullable"`
	PK       bool    `json:"pk" yaml:"pk"`
	Auto     bool    `json:"auto" yaml:"auto"`
	Default  *string `json:"default,omitempty" yaml:"default,omitempty"`
}

func newTableReport(info *internaldb.TableInfo, name string) tableReport {
	report := tableReport{Table: name, Columns: make([]columnReport, len(info.Columns))}
	for i, col := range info.Columns {
		report.Columns[i] = columnReport{
			Name:     col.Name,
			Type:     col.Type,
			Nullable: col.IsNullable,
			PK:       col.IsPK,
			Auto:     col.IsAuto,
		}
		if col.Default.Valid {
			def := col.Default.String
			report.Columns[i].Default = &def
		}
	}
	return report
}

// renderReports writes v (a tableReport or a schema-wide wrapper) in the
// requested format, or errors on formats the help does not advertise.
func renderStructured(v any) error {
	switch inspectFormat {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		defer enc.Close()
		return enc.Encode(v)
	default:
		return fmt.Errorf("unsupported --format %q (expected table|json|yaml)", inspectFormat)
	}
}

func runInspectSchema() error {
	if err := checkInspectFormat(); err != nil {
		return err
	}

	client, err := cli_db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	dialect := client.Dialect().Name()
	tables, err := listAllTables(client.Raw(), dialect)
	if err != nil {
		return fmt.Errorf("listing tables: %w", err)
	}

	if len(tables) == 0 {
		color.Yellow("No tables found in database.")
		return nil
	}

	if inspectFormat != "table" {
		schema := struct {
			Dialect string        `json:"dialect" yaml:"dialect"`
			Tables  []tableReport `json:"tables" yaml:"tables"`
		}{Dialect: dialect}
		for _, tableName := range tables {
			info, err := internaldb.GetTableInfo(client.Raw(), dialect, tableName)
			if err != nil {
				return fmt.Errorf("introspecting %s: %w", tableName, err)
			}
			schema.Tables = append(schema.Tables, newTableReport(info, tableName))
		}
		return renderStructured(schema)
	}

	color.Cyan("Database schema (%s) — %d tables\n", dialect, len(tables))

	for _, tableName := range tables {
		info, err := internaldb.GetTableInfo(client.Raw(), dialect, tableName)
		if err != nil {
			color.Yellow("  [!] Could not introspect %s: %v", tableName, err)
			continue
		}

		fmt.Printf("\n  %s (%d columns)\n", color.GreenString(tableName), len(info.Columns))

		tw := tablewriter.NewWriter(os.Stdout)
		tw.Header([]string{"Column", "Type", "Nullable", "PK", "Auto"})
		for _, col := range info.Columns {
			tw.Append([]string{
				col.Name, col.Type,
				fmt.Sprintf("%v", col.IsNullable),
				fmt.Sprintf("%v", col.IsPK),
				fmt.Sprintf("%v", col.IsAuto),
			})
		}
		tw.Render()
	}
	return nil
}

func runInspectTable(name string) error {
	if err := checkInspectFormat(); err != nil {
		return err
	}

	client, err := cli_db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	info, err := internaldb.GetTableInfo(client.Raw(), client.Dialect().Name(), name)
	if err != nil {
		return fmt.Errorf("introspecting table %s: %w", name, err)
	}
	// Introspection returns an empty column set for a nonexistent table on
	// most engines — rendering an empty report with exit 0 read as success
	// (QK-P1-5). Same guard as validate.
	if len(info.Columns) == 0 {
		return fmt.Errorf("table %q not found or has no columns", name)
	}

	if inspectFormat != "table" {
		return renderStructured(newTableReport(info, name))
	}

	fmt.Printf("Table: %s\n", name)
	table := tablewriter.NewWriter(os.Stdout)
	table.Header([]string{"Column", "Type", "Nullable", "PK", "Auto", "Default"})

	for _, col := range info.Columns {
		table.Append([]string{
			col.Name,
			col.Type,
			fmt.Sprintf("%v", col.IsNullable),
			fmt.Sprintf("%v", col.IsPK),
			fmt.Sprintf("%v", col.IsAuto),
			col.Default.String,
		})
	}
	table.Render()
	return nil
}

// checkInspectFormat rejects unknown --format values up front, before any
// database work happens.
func checkInspectFormat() error {
	switch inspectFormat {
	case "table", "json", "yaml":
		return nil
	default:
		return fmt.Errorf("unsupported --format %q (expected table|json|yaml)", inspectFormat)
	}
}

func runInspectSQL() error {
	if inspectModel == "" {
		return fmt.Errorf("specify a table name with --model <table>")
	}

	client, err := cli_db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	dialect := client.Dialect().Name()
	info, err := internaldb.GetTableInfo(client.Raw(), dialect, inspectModel)
	if err != nil {
		return fmt.Errorf("introspecting table %s: %w", inspectModel, err)
	}

	if len(info.Columns) == 0 {
		color.Yellow("Table %q not found or has no columns.", inspectModel)
		return nil
	}

	color.Cyan("-- Reconstructed CREATE TABLE for live table: %s (%s)\n", inspectModel, dialect)
	fmt.Printf("CREATE TABLE %s (\n", inspectModel)
	for i, col := range info.Columns {
		nullStr := "NOT NULL"
		if col.IsNullable {
			nullStr = "NULL"
		}
		pkStr := ""
		if col.IsPK {
			pkStr = " PRIMARY KEY"
		}
		comma := ","
		if i == len(info.Columns)-1 {
			comma = ""
		}
		fmt.Printf("  %-20s %-20s %s%s%s\n", col.Name, col.Type, nullStr, pkStr, comma)
	}
	fmt.Println(");")
	return nil
}

func listAllTables(db *sql.DB, dialect string) ([]string, error) {
	var query string
	switch dialect {
	case "postgres", "postgresql", "pgx":
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`
	case "mysql", "mariadb":
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name`
	case "sqlite", "sqlite3":
		query = `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	case "mssql", "sqlserver":
		query = `SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME`
	case "oracle":
		query = `SELECT table_name FROM user_tables ORDER BY table_name`
	default:
		return nil, fmt.Errorf("unsupported dialect for table listing: %s", dialect)
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

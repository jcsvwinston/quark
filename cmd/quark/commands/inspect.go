package commands

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/fatih/color"
	cli_db "github.com/jcsvwinston/quark/cmd/quark/internal/db"
	internaldb "github.com/jcsvwinston/quark/internal/db"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
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
	inspectSQLCmd.Flags().StringVar(&inspectModel, "model", "", "Model name")

	rootCmd.AddCommand(inspectCmd)
}

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Database introspection tools",
}

var inspectSchemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Show full database schema",
	Run: func(cmd *cobra.Command, args []string) {
		runInspectSchema()
	},
}

var inspectTableCmd = &cobra.Command{
	Use:   "table <name>",
	Short: "Show structure of a specific table",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runInspectTable(args[0])
	},
}

var inspectSQLCmd = &cobra.Command{
	Use:   "sql",
	Short: "Show generated SQL for a model",
	Run: func(cmd *cobra.Command, args []string) {
		runInspectSQL()
	},
}

func runInspectSchema() {
	client, err := cli_db.GetQuarkClient()
	if err != nil {
		color.Red("Error: %v", err)
		return
	}
	defer client.Close()

	dialect := client.Dialect().Name()
	tables, err := listAllTables(client.Raw(), dialect)
	if err != nil {
		color.Red("Error listing tables: %v", err)
		return
	}

	if len(tables) == 0 {
		color.Yellow("No tables found in database.")
		return
	}

	color.Cyan("Database schema (%s) — %d tables\n", dialect, len(tables))

	for _, tableName := range tables {
		info, err := internaldb.GetTableInfo(client.Raw(), dialect, tableName)
		if err != nil {
			color.Yellow("  [!] Could not introspect %s: %v", tableName, err)
			continue
		}

		fmt.Printf("\n  %s (%d columns)\n", color.GreenString(tableName), len(info.Columns))

		if inspectFormat == "table" {
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
	}
}

func runInspectTable(name string) {
	client, err := cli_db.GetQuarkClient()
	if err != nil {
		color.Red("Error: %v", err)
		return
	}

	info, err := internaldb.GetTableInfo(client.Raw(), client.Dialect().Name(), name)
	if err != nil {
		color.Red("Error introspecting table %s: %v", name, err)
		return
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
}

func runInspectSQL() {
	if inspectModel == "" {
		color.Red("Error: specify a table name with --model <table>")
		return
	}

	client, err := cli_db.GetQuarkClient()
	if err != nil {
		color.Red("Error: %v", err)
		return
	}
	defer client.Close()

	dialect := client.Dialect().Name()
	info, err := internaldb.GetTableInfo(client.Raw(), dialect, inspectModel)
	if err != nil {
		color.Red("Error introspecting table %s: %v", inspectModel, err)
		return
	}

	if len(info.Columns) == 0 {
		color.Yellow("Table %q not found or has no columns.", inspectModel)
		return
	}

	color.Cyan("-- Generated CREATE TABLE for: %s (%s)\n", inspectModel, dialect)
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
}

func listAllTables(db *sql.DB, dialect string) ([]string, error) {
	var query string
	switch dialect {
	case "postgres", "postgresql", "pgx":
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`
	case "mysql":
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

package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	cli_db "github.com/jcsvwinston/quark/cmd/quark/internal/db"
	internaldb "github.com/jcsvwinston/quark/internal/db"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	validateStrict bool
	validateTable  string
)

func init() {
	validateCmd.Flags().BoolVar(&validateStrict, "strict", false, "Exit with error if DB has unmapped columns")
	validateCmd.Flags().StringVar(&validateTable, "table", "", "Table name to validate")
	rootCmd.AddCommand(validateCmd)
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate a table schema and report column mapping",
	Long: `Connects to the configured database and introspects a table.
Reports each column with its type, nullability, PK and auto status.
Use --strict to exit with a non-zero code when unmapped columns are found.`,
	Run: func(cmd *cobra.Command, args []string) {
		if validateTable == "" && len(args) > 0 {
			validateTable = args[0]
		}
		if validateTable == "" {
			color.Red("Error: specify a table name with --table <name> or as an argument")
			os.Exit(1)
		}
		runValidate()
	},
}

func runValidate() {
	client, err := cli_db.GetQuarkClient()
	if err != nil {
		color.Red("Error connecting to database: %v", err)
		os.Exit(1)
	}
	defer client.Close()

	dialect := client.Dialect().Name()
	info, err := internaldb.GetTableInfo(client.Raw(), dialect, validateTable)
	if err != nil {
		color.Red("Error introspecting table %q: %v", validateTable, err)
		os.Exit(1)
	}

	if len(info.Columns) == 0 {
		color.Yellow("Table %q not found or has no columns.", validateTable)
		os.Exit(1)
	}

	color.Cyan("Table: %s  (dialect: %s)\n", validateTable, dialect)

	tw := tablewriter.NewWriter(os.Stdout)
	tw.Header([]string{"Column", "DB Type", "Nullable", "PK", "Auto", "Default", "Status"})

	// Heuristic: known quark-managed column names
	knownQuark := map[string]bool{
		"id": true, "created_at": true, "updated_at": true,
		"deleted_at": true, "tenant_id": true,
	}

	issues := 0
	for _, col := range info.Columns {
		status := color.GreenString("✓ mapped")
		colLower := strings.ToLower(col.Name)
		if !knownQuark[colLower] {
			status = color.YellowString("? review")
			issues++
		}
		if col.IsPK {
			status = color.GreenString("✓ pk")
		}

		def := col.Default.String
		if !col.Default.Valid {
			def = "-"
		}

		tw.Append([]string{
			col.Name,
			col.Type,
			fmt.Sprintf("%v", col.IsNullable),
			fmt.Sprintf("%v", col.IsPK),
			fmt.Sprintf("%v", col.IsAuto),
			def,
			status,
		})
	}
	tw.Render()

	fmt.Printf("\nTotal columns: %d", len(info.Columns))
	if issues > 0 {
		fmt.Printf("  |  Columns to review: %d\n", issues)
		color.Yellow("Tip: annotate your Go struct fields with `db:\"<column>\"` tags to map them.")
		if validateStrict {
			os.Exit(1)
		}
	} else {
		fmt.Println()
		color.Green("All columns accounted for.")
	}
}

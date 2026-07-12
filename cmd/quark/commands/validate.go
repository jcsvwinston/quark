package commands

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cmd/quark/internal/codegen"
	cli_db "github.com/jcsvwinston/quark/cmd/quark/internal/db"
	internaldb "github.com/jcsvwinston/quark/internal/db"
	"github.com/jcsvwinston/quark/internal/schema"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	validateStrict bool
	validateTable  string
	validateModels string
	validateModel  string
)

func init() {
	validateCmd.Flags().BoolVar(&validateStrict, "strict", false, "Exit with error if DB has columns unmapped by the model")
	validateCmd.Flags().StringVar(&validateTable, "table", "", "Table name to validate")
	validateCmd.Flags().StringVar(&validateModels, "models", "", "go/packages pattern where the model structs live (default: paths.models from config, else ./...)")
	validateCmd.Flags().StringVar(&validateModel, "model", "", "Model struct name (default: derived from the table name)")
	rootCmd.AddCommand(validateCmd)
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate a Go model against the live table schema",
	Long: `Loads your model structs with go/packages, introspects the table in the
configured database, and reports the column mapping in both directions:
DB columns with no db-tagged field, and fields whose column is missing in
the database.

A field missing in the database always exits non-zero. Use --strict to also
fail on DB columns the model does not map.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if validateTable == "" && len(args) > 0 {
			validateTable = args[0]
		}
		if validateTable == "" {
			return fmt.Errorf("specify a table name with --table <name> or as an argument")
		}
		return runValidate()
	},
}

// findModel locates the model struct to validate: --model by name, otherwise
// the struct whose conventional table name (pluralized snake_case) matches
// the requested table.
func findModel(pkgs []codegen.PackageModels, table, name string) (*codegen.ModelDef, []string) {
	var available []string
	var found *codegen.ModelDef
	for i := range pkgs {
		for j := range pkgs[i].Models {
			m := &pkgs[i].Models[j]
			available = append(available, m.Name)
			if name != "" {
				if m.Name == name {
					found = m
				}
				continue
			}
			if schema.Pluralize(schema.ToSnakeCase(m.Name)) == table {
				found = m
			}
		}
	}
	sort.Strings(available)
	return found, available
}

func runValidate() error {
	// Load the Go models first: no point connecting anywhere if the structs
	// cannot be found. This replaces the old hardcoded name heuristic that
	// never read a single Go file (QK-P1-2).
	patterns := validateModels
	if patterns == "" {
		patterns = viper.GetString("paths.models")
	}
	if patterns == "" {
		patterns = "./..."
	}
	// When --models names an on-disk directory, anchor go/packages module
	// resolution there (the models may live outside the cwd's module).
	var pkgs []codegen.PackageModels
	var err error
	if st, statErr := os.Stat(patterns); statErr == nil && st.IsDir() {
		pkgs, err = codegen.LoadDir(patterns, "./...")
	} else {
		pkgs, err = codegen.Load(patterns)
	}
	if err != nil {
		return fmt.Errorf("loading model packages %q: %w", patterns, err)
	}
	model, available := findModel(pkgs, validateTable, validateModel)
	if model == nil {
		if len(available) == 0 {
			return fmt.Errorf("no model structs (structs with `db` tags) found under %q — point --models at the package that defines them", patterns)
		}
		if validateModel != "" {
			return fmt.Errorf("model %q not found under %q; discovered models: %s", validateModel, patterns, strings.Join(available, ", "))
		}
		return fmt.Errorf("no model matching table %q under %q (looked for a struct whose pluralized snake_case name is %q); discovered models: %s — name one explicitly with --model", validateTable, patterns, validateTable, strings.Join(available, ", "))
	}

	client, err := cli_db.GetQuarkClient()
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer client.Close()

	dialect := client.Dialect().Name()
	info, err := internaldb.GetTableInfo(client.Raw(), dialect, validateTable)
	if err != nil {
		return fmt.Errorf("introspecting table %q: %w", validateTable, err)
	}
	if len(info.Columns) == 0 {
		return fmt.Errorf("table %q not found or has no columns", validateTable)
	}

	// Index the model's columns (db tags) for the two-way comparison.
	fieldByColumn := make(map[string]quark.ModelField, len(model.Fields))
	for _, f := range model.Fields {
		fieldByColumn[strings.ToLower(f.Column)] = f
	}
	dbColumns := make(map[string]bool, len(info.Columns))
	for _, col := range info.Columns {
		dbColumns[strings.ToLower(col.Name)] = true
	}

	color.Cyan("Table: %s  ↔  Model: %s  (dialect: %s)\n", validateTable, model.Name, dialect)

	tw := tablewriter.NewWriter(os.Stdout)
	tw.Header([]string{"Column", "DB Type", "Nullable", "PK", "Go Field", "Go Type", "Status"})

	unmapped := 0
	for _, col := range info.Columns {
		status := color.GreenString("✓ mapped")
		goField, goType := "-", "-"
		if f, ok := fieldByColumn[strings.ToLower(col.Name)]; ok {
			goField, goType = f.Name, f.GoType
			if col.IsPK {
				status = color.GreenString("✓ pk")
			}
		} else {
			status = color.YellowString("✗ unmapped in Go")
			unmapped++
		}
		tw.Append([]string{
			col.Name,
			col.Type,
			fmt.Sprintf("%v", col.IsNullable),
			fmt.Sprintf("%v", col.IsPK),
			goField,
			goType,
			status,
		})
	}

	// Fields whose column does not exist in the table — the direction the
	// old heuristic could never see.
	missing := 0
	for _, f := range model.Fields {
		if !dbColumns[strings.ToLower(f.Column)] {
			tw.Append([]string{
				f.Column,
				"-", "-", "-",
				f.Name,
				f.GoType,
				color.RedString("✗ missing in DB"),
			})
			missing++
		}
	}
	tw.Render()

	fmt.Printf("\nDB columns: %d  |  Model fields: %d\n", len(info.Columns), len(model.Fields))

	if missing > 0 {
		return fmt.Errorf("%d model field(s) have no matching column in %q — run your migrations or fix the db tags", missing, validateTable)
	}
	if unmapped > 0 {
		color.Yellow("%d DB column(s) are not mapped by %s. Add `db:\"<column>\"` tags if the model should own them.", unmapped, model.Name)
		if validateStrict {
			return fmt.Errorf("--strict: %d unmapped column(s) in %q", unmapped, validateTable)
		}
	} else {
		color.Green("All columns accounted for.")
	}
	return nil
}

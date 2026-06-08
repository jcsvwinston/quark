package commands

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/jcsvwinston/quark/cmd/quark/internal/gen"
	internaldb "github.com/jcsvwinston/quark/internal/db"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	modelFromTable string
	modelFields    string
	modelOutDir    string
	modelPackage   string
	modelDialect   string
	modelTags      string
)

func init() {
	modelCmd.AddCommand(genCmd)

	genCmd.Flags().StringVar(&modelFromTable, "from-table", "", "Tables to generate (comma-separated)")
	genCmd.Flags().StringVar(&modelFields, "fields", "", "Field definitions (e.g. 'id:int64,email:string')")
	genCmd.Flags().StringVar(&modelOutDir, "out", "./models", "Output directory")
	genCmd.Flags().StringVar(&modelPackage, "package", "models", "Package name")
	genCmd.Flags().StringVar(&modelDialect, "dialect", "", "Override dialect")
	genCmd.Flags().StringVar(&modelTags, "tags", "json", "Additional tags")

	rootCmd.AddCommand(modelCmd)
}

var modelCmd = &cobra.Command{
	Use:   "model",
	Short: "Manage Quark models",
}

var genCmd = &cobra.Command{
	Use:     "generate [Name]",
	Aliases: []string{"gen"},
	Short:   "Generate models from tables or definition",
	// A generation failure must surface as a non-zero exit (main.go prints the
	// returned error and exits 1). Silence cobra's own usage/error dump so the
	// single error line from main is the only output.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runModelGen(args)
	},
}

func runModelGen(args []string) error {
	wantFromTable := modelFromTable != ""
	wantFromFields := len(args) > 0 && modelFields != ""
	if !wantFromTable && !wantFromFields {
		return fmt.Errorf("specify either --from-table or a model name with --fields")
	}

	// Both paths write into modelOutDir; create it once here so they behave
	// consistently (generateFromDefinition historically skipped this and failed
	// silently when --out did not exist).
	if err := os.MkdirAll(modelOutDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory %q: %w", modelOutDir, err)
	}

	if wantFromTable {
		return generateFromTables()
	}
	return generateFromDefinition(args[0])
}

func generateFromTables() error {
	dialect := modelDialect
	if dialect == "" {
		dialect = viper.GetString("database.default.driver")
	}
	dsn := viper.GetString("database.default.dsn")

	if dialect == "" || dsn == "" {
		return fmt.Errorf("database configuration missing: run 'quark init' or specify --dialect and configure DSN")
	}

	sqlDB, err := sql.Open(dialect, dsn)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer sqlDB.Close()

	tables := strings.Split(modelFromTable, ",")

	generator, err := gen.NewModelGenerator(modelPackage, modelOutDir, modelTemplate)
	if err != nil {
		return fmt.Errorf("initializing generator: %w", err)
	}

	// Process every requested table; a per-table failure is logged and skipped,
	// but the first one is remembered so the command still exits non-zero.
	var firstErr error
	for _, tableName := range tables {
		tableName = strings.TrimSpace(tableName)
		info, err := internaldb.GetTableInfo(sqlDB, dialect, tableName)
		if err != nil {
			color.Red("Error introspecting table %s: %v", tableName, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("introspecting table %s: %w", tableName, err)
			}
			continue
		}

		genTable := gen.TableInfo{
			Name:    info.Name,
			Columns: make([]gen.ColumnInfo, len(info.Columns)),
		}
		for i, col := range info.Columns {
			genTable.Columns[i] = gen.ColumnInfo{
				Name:       col.Name,
				Type:       col.Type,
				IsNullable: col.IsNullable,
				IsPK:       col.IsPK,
				IsAuto:     col.IsAuto,
				Default:    col.Default.String,
			}
		}

		if err := generator.GenerateFromTable(genTable); err != nil {
			color.Red("Error generating model for %s: %v", tableName, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("generating model for %s: %w", tableName, err)
			}
			continue
		}
		fmt.Printf("Generated model for table: %s\n", tableName)
	}
	return firstErr
}

func generateFromDefinition(name string) error {
	fields := strings.Split(modelFields, ",")
	data := gen.ModelData{
		Package:    modelPackage,
		StructName: name,
		TableName:  strings.ToLower(name) + "s",
	}

	for _, f := range fields {
		parts := strings.Split(f, ":")
		if len(parts) != 2 {
			return fmt.Errorf("invalid field definition %q: use name:type", f)
		}

		fieldName := parts[0]
		fieldType := parts[1]

		quarkTag := ""
		if fieldName == "id" {
			quarkTag = "pk,auto"
		}

		data.Fields = append(data.Fields, gen.FieldData{
			Name:     gen.SnakeToCamel(fieldName, true),
			Type:     fieldType,
			QuarkTag: quarkTag,
			JSONTag:  fieldName,
		})
	}

	generator, err := gen.NewModelGenerator(modelPackage, modelOutDir, modelTemplate)
	if err != nil {
		return fmt.Errorf("initializing generator: %w", err)
	}

	if err := generator.GenerateFromData(data); err != nil {
		return fmt.Errorf("generating model: %w", err)
	}
	fmt.Printf("Generated model from definition: %s\n", name)
	return nil
}

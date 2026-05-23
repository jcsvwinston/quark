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
	Run: func(cmd *cobra.Command, args []string) {
		runModelGen(args)
	},
}

func runModelGen(args []string) {
	if modelFromTable != "" {
		generateFromTables()
	} else if len(args) > 0 && modelFields != "" {
		generateFromDefinition(args[0])
	} else {
		color.Red("Error: specify either --from-table or a model name with --fields")
		os.Exit(1)
	}
}

func generateFromTables() {
	dialect := modelDialect
	if dialect == "" {
		dialect = viper.GetString("database.default.driver")
	}
	dsn := viper.GetString("database.default.dsn")

	if dialect == "" || dsn == "" {
		color.Red("Error: database configuration missing. Run 'quark init' or specify --dialect and configure DSN.")
		return
	}

	sqlDB, err := sql.Open(dialect, dsn)
	if err != nil {
		color.Red("Error connecting to database: %v", err)
		return
	}
	defer sqlDB.Close()

	tables := strings.Split(modelFromTable, ",")

	generator, err := gen.NewModelGenerator(modelPackage, modelOutDir, modelTemplate)
	if err != nil {
		color.Red("Error initializing generator: %v", err)
		return
	}

	if err := os.MkdirAll(modelOutDir, 0755); err != nil {
		color.Red("Error creating output directory: %v", err)
		return
	}

	for _, tableName := range tables {
		tableName = strings.TrimSpace(tableName)
		info, err := internaldb.GetTableInfo(sqlDB, dialect, tableName)
		if err != nil {
			color.Red("Error introspecting table %s: %v", tableName, err)
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
			continue
		}
		fmt.Printf("Generated model for table: %s\n", tableName)
	}
}

func generateFromDefinition(name string) {
	fields := strings.Split(modelFields, ",")
	data := gen.ModelData{
		Package:    modelPackage,
		StructName: name,
		TableName:  strings.ToLower(name) + "s",
	}

	for _, f := range fields {
		parts := strings.Split(f, ":")
		if len(parts) != 2 {
			color.Red("Error: invalid field definition %s. Use name:type", f)
			continue
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
		color.Red("Error initializing generator: %v", err)
		return
	}

	if err := generator.GenerateFromData(data); err != nil {
		color.Red("Error generating model: %v", err)
		return
	}
	fmt.Printf("Generated model from definition: %s\n", name)
}

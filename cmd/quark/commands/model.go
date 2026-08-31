package commands

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	clidb "github.com/jcsvwinston/quark/cmd/quark/internal/db"
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
	genCmd.Flags().StringVar(&modelFields, "fields", "", "Field definitions: name:type[:not_null|unique|version]; rich types nullable<T>, array<T>, json<T>, belongs_to<Model> (e.g. 'id:int64,email:string:unique,tags:array<string>')")
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
	Use: "generate [Name]",
	Example: `  quark model generate --from-table users,orders --out ./models
  quark model generate Product --fields "id:int64,name:string,price:float64" --out ./models`,
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

	// dialect stays as configured for GetTableInfo (which understands dialect
	// names); only sql.Open needs the registered driver name.
	sqlDB, err := sql.Open(clidb.DriverName(dialect), dsn)
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
		// --from-table values are user input interpolated into introspection
		// SQL; guard the identifier before it reaches the query (AQ-09/QC-3).
		if err := validateTableName(tableName); err != nil {
			color.Red("%v", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		info, err := internaldb.GetTableInfo(sqlDB, dialect, tableName)
		if err != nil {
			color.Red("Error introspecting table %s: %v", tableName, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("introspecting table %s: %w", tableName, err)
			}
			continue
		}
		// A nonexistent table introspects as zero columns on most engines;
		// generating an empty struct with exit 0 read as success (QK-P1-5).
		if len(info.Columns) == 0 {
			color.Red("Error: table %s not found or has no columns", tableName)
			if firstErr == nil {
				firstErr = fmt.Errorf("table %s not found or has no columns", tableName)
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
		if len(parts) != 2 && len(parts) != 3 {
			return fmt.Errorf("invalid field definition %q: use name:type or name:type:modifier (modifiers: not_null, unique, version; rich types: nullable<T>, array<T>, json<T>, belongs_to<Model>)", f)
		}

		fieldName := parts[0]
		fieldType := parts[1]

		// Modifier segment (DX-19): a token from the ORM's quark-tag
		// vocabulary.
		quarkTag := ""
		if len(parts) == 3 {
			switch parts[2] {
			case "not_null", "unique", "version":
				quarkTag = parts[2]
			default:
				return fmt.Errorf("invalid field modifier %q in %q: expected not_null, unique or version", parts[2], f)
			}
		}

		// Rich-type vocabulary (DX-19): quark generic containers and the
		// belongs_to relation pair.
		if inner, ok := genericArg(fieldType, "belongs_to"); ok {
			fkCol := fieldName + "_id"
			data.Fields = append(data.Fields,
				gen.FieldData{
					Name:    gen.SnakeToCamel(fkCol, true),
					Type:    "int64",
					JSONTag: fkCol,
				},
				gen.FieldData{
					Name:    gen.SnakeToCamel(fieldName, true),
					Type:    inner,
					RelTag:  "belongs_to",
					JoinTag: fkCol,
				},
			)
			continue
		}
		for spec, container := range map[string]string{
			"nullable": "quark.Nullable[%s]",
			"array":    "quark.Array[%s]",
			"json":     "quark.JSON[%s]",
		} {
			if inner, ok := genericArg(fieldType, spec); ok {
				fieldType = fmt.Sprintf(container, inner)
				break
			}
		}

		// `id` is the conventional primary key; the template renders IsPK as
		// pk:"true", the tag the ORM parses. (The old QuarkTag="pk,auto" was
		// vocabulary the ORM never understood, and the template dropped it
		// anyway — QCD-CLI-1.) No implicit quark:"not_null": unlike
		// from-table, a definition carries no nullability information — the
		// :not_null modifier is the explicit form.
		data.Fields = append(data.Fields, gen.FieldData{
			Name:     gen.SnakeToCamel(fieldName, true),
			Type:     fieldType,
			QuarkTag: quarkTag,
			JSONTag:  fieldName,
			IsPK:     fieldName == "id",
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

// genericArg extracts T from a spec of the form kind<T> (DX-19 rich-type
// vocabulary). Returns ok=false when the spec is not that kind.
func genericArg(spec, kind string) (string, bool) {
	if strings.HasPrefix(spec, kind+"<") && strings.HasSuffix(spec, ">") {
		inner := strings.TrimSpace(spec[len(kind)+1 : len(spec)-1])
		if inner != "" {
			return inner, true
		}
	}
	return "", false
}

package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type TableInfo struct {
	Name    string
	Columns []ColumnInfo
}

type ColumnInfo struct {
	Name       string
	Type       string
	IsNullable bool
	IsPK       bool
	IsAuto     bool
	Default    string
}

type ModelGenerator struct {
	PackageName string
	OutDir      string
	Template    *template.Template
}

type ModelData struct {
	Package           string
	StructName        string
	TableName         string
	Fields            []FieldData
	HasJSONRawMessage bool
	HasTimeField      bool
}

type FieldData struct {
	Name     string
	Type     string
	QuarkTag string
	JSONTag  string
	IsPK     bool
}

func NewModelGenerator(pkgName, outDir string, tmplStr string) (*ModelGenerator, error) {
	tmpl, err := template.New("model").Parse(tmplStr)
	if err != nil {
		return nil, err
	}
	return &ModelGenerator{
		PackageName: pkgName,
		OutDir:      outDir,
		Template:    tmpl,
	}, nil
}

func (g *ModelGenerator) GenerateFromData(data ModelData) error {
	var buf bytes.Buffer
	if err := g.Template.Execute(&buf, data); err != nil {
		return err
	}

	fileName := strings.ToLower(data.StructName) + ".go"
	path := filepath.Join(g.OutDir, fileName)
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func (g *ModelGenerator) GenerateFromTable(table TableInfo) error {
	normalizedTableName := strings.ToLower(table.Name)
	data := ModelData{
		Package:    g.PackageName,
		StructName: SnakeToCamel(normalizedTableName, true),
		TableName:  normalizedTableName,
	}

	for _, col := range table.Columns {
		normalizedName := strings.ToLower(col.Name)
		field := FieldData{
			Name:    SnakeToCamel(normalizedName, true),
			JSONTag: normalizedName,
		}

		goType, quarkTags := mapSQLToGo(col)
		field.Type = goType
		field.QuarkTag = strings.Join(quarkTags, ",")

		if goType == "json.RawMessage" {
			data.HasJSONRawMessage = true
		}
		if strings.Contains(goType, "time.Time") {
			data.HasTimeField = true
		}
		field.IsPK = col.IsPK

		data.Fields = append(data.Fields, field)
	}

	var buf bytes.Buffer
	if err := g.Template.Execute(&buf, data); err != nil {
		return err
	}

	fileName := strings.ToLower(data.StructName) + ".go"
	path := filepath.Join(g.OutDir, fileName)
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func mapSQLToGo(col ColumnInfo) (string, []string) {
	var goType string
	var tags []string

	if col.IsPK {
		tags = append(tags, "pk")
		if col.IsAuto {
			tags = append(tags, "auto")
		}
	}

	if !col.IsNullable {
		tags = append(tags, "notnull")
	}

	sqlType := strings.ToLower(col.Type)
	switch {
	case strings.Contains(sqlType, "bigint"), strings.Contains(sqlType, "int8"):
		goType = "int64"
	case strings.Contains(sqlType, "int"):
		goType = "int"
	case strings.Contains(sqlType, "bool"):
		goType = "bool"
	case strings.Contains(sqlType, "char"), strings.Contains(sqlType, "text"), strings.Contains(sqlType, "uuid"), strings.Contains(sqlType, "clob"):
		goType = "string"
	case strings.Contains(sqlType, "timestamp"), strings.Contains(sqlType, "datetime"), strings.Contains(sqlType, "date"), strings.HasSuffix(sqlType, "time"):
		goType = "time.Time"
	case strings.Contains(sqlType, "json"):
		goType = "json.RawMessage"
	case strings.Contains(sqlType, "decimal"), strings.Contains(sqlType, "numeric"), strings.Contains(sqlType, "float"), strings.Contains(sqlType, "double"), strings.Contains(sqlType, "real"):
		goType = "float64"
	// Oracle-specific
	case sqlType == "number":
		goType = "int64"
	case strings.Contains(sqlType, "varchar2"), strings.Contains(sqlType, "nvarchar2"), strings.Contains(sqlType, "nchar"):
		goType = "string"
	// MSSQL-specific
	case strings.Contains(sqlType, "nvarchar"), strings.Contains(sqlType, "ntext"):
		goType = "string"
	case strings.Contains(sqlType, "bit"):
		goType = "bool"
	case strings.Contains(sqlType, "money"), strings.Contains(sqlType, "smallmoney"):
		goType = "float64"
	default:
		goType = "string"
	}

	if col.IsNullable && goType != "json.RawMessage" {
		goType = "*" + goType
	}

	return goType, tags
}

func SnakeToCamel(s string, public bool) string {
	parts := strings.Split(s, "_")
	for i := range parts {
		if i == 0 && !public {
			continue
		}

		word := strings.ToLower(parts[i])
		if word == "id" {
			parts[i] = "ID"
		} else if word == "url" {
			parts[i] = "URL"
		} else {
			parts[i] = strings.Title(parts[i])
		}
	}
	return strings.Join(parts, "")
}

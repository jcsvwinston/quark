// Package schema provides struct reflection and model metadata caching for Quark ORM.
// It parses Go struct tags (db, pk, rel, join) and caches the result using sync.Map
// to ensure O(1) lookups after the first access per model type.
package schema

import (
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// RelationMeta holds metadata about a model relation.
type RelationMeta struct {
	Type           string       // "has_one", "has_many", "belongs_to", "m2m", "polymorphic"
	Field          string       // struct field name
	JoinCol        string       // foreign key column (for belongs_to, has_one, has_many)
	JoinTable      string       // join table name (for m2m)
	JoinFK         string       // foreign key in join table pointing to this model (for m2m)
	JoinRefFK      string       // foreign key in join table pointing to related model (for m2m)
	PolyType       string       // polymorphic type identifier (for polymorphic)
	PolyTypeColumn string       // column storing the polymorphic type (for polymorphic)
	PolyIDColumn   string       // column storing the polymorphic foreign key (for polymorphic)
	RefType        reflect.Type // type of the related model (the struct type)
	IsSlice        bool         // true for has_many, m2m
}

// PKMeta holds primary key metadata for a single PK column.
type PKMeta struct {
	Column string
	Index  int
	Kind   reflect.Kind
}

// IsComposite returns true when the model uses a multi-column primary key.
// Use ModelMeta.CompositePK instead of ModelMeta.PK when this is true.
func (p PKMeta) IsComposite() bool { return false } // sentinel; see ModelMeta.HasCompositePK

// FindPK finds the primary key field in a struct value.
// It first looks for a pk:"true" tag, then falls back to db:"id".
// When multiple fields carry pk:"true" the first one is returned for
// backward-compatibility; use FindPKs to obtain all of them.
func FindPK(v reflect.Value) (PKMeta, bool) {
	pks := FindPKs(v)
	if len(pks) == 0 {
		return PKMeta{}, false
	}
	return pks[0], true
}

// FindPKs returns all primary key fields from a struct value.
// Fields tagged with pk:"true" are returned in declaration order.
// When no pk:"true" tag is present it falls back to the single db:"id" field.
func FindPKs(v reflect.Value) []PKMeta {
	t := v.Type()

	var pks []PKMeta
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Tag.Get("pk") == "true" {
			dbTag := ColumnFromDBTag(field.Tag.Get("db"))
			if dbTag == "" || dbTag == "-" {
				dbTag = ToSnakeCase(field.Name)
			}
			pks = append(pks, PKMeta{Column: dbTag, Index: i, Kind: field.Type.Kind()})
		}
	}
	if len(pks) > 0 {
		return pks
	}

	// Fallback: db:"id"
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if ColumnFromDBTag(field.Tag.Get("db")) == "id" {
			return []PKMeta{{Column: "id", Index: i, Kind: field.Type.Kind()}}
		}
	}

	return nil
}

// ModelMeta holds cached metadata about a model struct.
// Computed once per type and stored in a global registry.
type ModelMeta struct {
	Table          string
	PK             PKMeta
	HasPK          bool
	CompositePK    []PKMeta // populated when two or more fields carry pk:"true"
	HasCompositePK bool     // true when len(CompositePK) > 1
	Fields         []FieldMeta
	FieldByCol     map[string]*FieldMeta    // lookup by db column name
	Relations      map[string]*RelationMeta // lookup by field name

	// VersionFieldIndex is the FieldMeta index of the optimistic-locking
	// version column, or -1 when the model does not have one. Cached at
	// schema-compute time so the hot Update / Save paths don't have to
	// re-scan Fields.
	VersionFieldIndex int
}

// FieldMeta holds metadata about a single struct field.
type FieldMeta struct {
	Index     int
	Column    string // value of the db:"" tag (without options)
	Kind      reflect.Kind
	Type      reflect.Type
	IsPK      bool
	OldColumn string // for renames
	NotNull   bool   // from tag: quark:"not_null" or nullable:"false"
	Default   string // from tag: default:"value"
	Unique    bool   // from tag: quark:"unique"

	// SQL-type sizing options parsed from the db tag, e.g.
	//   db:"name,size=512"
	//   db:"price,precision=18,scale=4"
	// A zero value means "use the dialect default for the Go type". The
	// migrate layer applies these to VARCHAR/CHAR sizing and DECIMAL
	// precision/scale; custom type mappers can read them via TypeOptions.
	Size      int
	Precision int
	Scale     int

	// IsVersion marks the field as the optimistic-locking version column.
	// Set by quark:"version". When present, Update / UpdateFields /
	// Tracked.Save include "version = version + 1" in SET and
	// "AND version = ?" in WHERE; a zero rows-affected on the response
	// surfaces ErrStaleEntity. Only one field per model may carry this tag.
	IsVersion bool
}

// modelRegistry caches ModelMeta by reflect.Type.
var modelRegistry sync.Map // map[reflect.Type]*ModelMeta

// GetModelMeta returns the cached metadata for model type T.
// If not cached, it computes and stores it.
func GetModelMeta[T any]() *ModelMeta {
	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Fast path: already cached
	if cached, ok := modelRegistry.Load(t); ok {
		return cached.(*ModelMeta)
	}

	// Slow path: compute metadata
	meta := computeModelMeta(t)
	actual, _ := modelRegistry.LoadOrStore(t, meta)
	return actual.(*ModelMeta)
}

// GetModelMetaByType returns the cached metadata for a reflect.Type.
func GetModelMetaByType(t reflect.Type) *ModelMeta {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if cached, ok := modelRegistry.Load(t); ok {
		return cached.(*ModelMeta)
	}

	meta := computeModelMeta(t)
	actual, _ := modelRegistry.LoadOrStore(t, meta)
	return actual.(*ModelMeta)
}

// TableNamer interface for custom table names.
type TableNamer interface {
	TableName() string
}

// computeModelMeta builds ModelMeta from a reflect.Type.
func computeModelMeta(t reflect.Type) *ModelMeta {
	tableName := ToSnakeCase(Pluralize(t.Name()))

	// Check if type implements TableName() string
	// We create a zero value of the type to check for methods
	zero := reflect.New(t).Interface()
	if tn, ok := zero.(TableNamer); ok {
		tableName = tn.TableName()
	}

	meta := &ModelMeta{
		Table:             tableName,
		FieldByCol:        make(map[string]*FieldMeta),
		Relations:         make(map[string]*RelationMeta),
		VersionFieldIndex: -1,
	}

	// Find PKs: collect all pk:"true" tags; fall back to db:"id"
	var pkIndices []int
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("pk") == "true" {
			pkIndices = append(pkIndices, i)
		}
	}
	if len(pkIndices) == 0 {
		for i := 0; i < t.NumField(); i++ {
			if ColumnFromDBTag(t.Field(i).Tag.Get("db")) == "id" {
				pkIndices = append(pkIndices, i)
				break
			}
		}
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Parse relations
		relTag := field.Tag.Get("rel")
		if relTag != "" {
			joinCol := field.Tag.Get("join")
			isSlice := field.Type.Kind() == reflect.Slice

			refType := field.Type
			if isSlice {
				refType = refType.Elem()
			}
			if refType.Kind() == reflect.Ptr {
				refType = refType.Elem()
			}

			relMeta := &RelationMeta{
				Type:    relTag,
				Field:   field.Name,
				JoinCol: joinCol,
				RefType: refType,
				IsSlice: isSlice,
			}

			// Infer JoinCol if missing for standard relations
			if relMeta.JoinCol == "" {
				if relMeta.Type == "belongs_to" {
					// For belongs_to, the FK is in THIS model (pointing to related model)
					relMeta.JoinCol = ToSnakeCase(refType.Name()) + "_id"
				} else if relMeta.Type == "has_one" || relMeta.Type == "has_many" {
					// For has_one/has_many, the FK is in the RELATED model (pointing to this model)
					relMeta.JoinCol = ToSnakeCase(t.Name()) + "_id"
				}
			}

			// Parse m2m (many-to-many) tag: m2m:"join_table" or m2m:"join_table:this_fk:ref_fk"
			if m2mTag := field.Tag.Get("m2m"); m2mTag != "" {
				parts := strings.Split(m2mTag, ":")
				relMeta.JoinTable = parts[0]
				if len(parts) >= 3 {
					relMeta.JoinFK = parts[1]
					relMeta.JoinRefFK = parts[2]
				}
				// Auto-generate fk names if not specified
				if relMeta.JoinFK == "" {
					relMeta.JoinFK = ToSnakeCase(t.Name()) + "_id"
				}
				if relMeta.JoinRefFK == "" {
					relMeta.JoinRefFK = ToSnakeCase(refType.Name()) + "_id"
				}
			}

			// Parse polymorphic tag: polymorphic:"type_col:poly_type" or polymorphic:"poly_type"
			if polyTag := field.Tag.Get("polymorphic"); polyTag != "" {
				parts := strings.Split(polyTag, ":")
				if len(parts) == 2 {
					relMeta.PolyTypeColumn = parts[0]
					relMeta.PolyType = parts[1]
				} else {
					relMeta.PolyType = parts[0]
					relMeta.PolyTypeColumn = "poly_type"
				}
				// Use the join tag value as the PolyIDColumn if provided,
				// otherwise derive from field name.
				if joinCol != "" {
					relMeta.PolyIDColumn = joinCol
				} else {
					relMeta.PolyIDColumn = ToSnakeCase(field.Name) + "_id"
				}
			}

			meta.Relations[field.Name] = relMeta
			continue
		}

		dbTag := field.Tag.Get("db")
		if dbTag == "" || dbTag == "-" {
			continue
		}

		// Split the db tag into the column name plus optional sizing
		// options: db:"name,size=512" or db:"price,precision=18,scale=4".
		colName, fieldSize, fieldPrecision, fieldScale := parseDBTag(dbTag)
		dbTag = colName

		isPK := false
		for _, idx := range pkIndices {
			if i == idx {
				isPK = true
				break
			}
		}
		oldCol := ""
		notNull := isPK // PKs are always NOT NULL
		defaultVal := ""
		unique := false
		isVersion := false
		if quarkTag := field.Tag.Get("quark"); quarkTag != "" {
			for _, part := range strings.Split(quarkTag, ",") {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "rename:") {
					oldCol = strings.TrimPrefix(part, "rename:")
				} else if part == "not_null" {
					notNull = true
				} else if part == "unique" {
					unique = true
				} else if part == "version" {
					isVersion = true
					// version columns must be NOT NULL — a NULL version
					// can't be incremented and would defeat the lock.
					notNull = true
				}
			}
		}
		if nullable := field.Tag.Get("nullable"); nullable == "false" {
			notNull = true
		}
		if def := field.Tag.Get("default"); def != "" {
			defaultVal = def
		}

		fm := FieldMeta{
			Index:     i,
			Column:    dbTag,
			Kind:      field.Type.Kind(),
			Type:      field.Type,
			IsPK:      isPK,
			OldColumn: oldCol,
			NotNull:   notNull,
			Default:   defaultVal,
			Unique:    unique,
			Size:      fieldSize,
			Precision: fieldPrecision,
			Scale:     fieldScale,
			IsVersion: isVersion,
		}
		meta.Fields = append(meta.Fields, fm)
		meta.FieldByCol[strings.ToLower(dbTag)] = &meta.Fields[len(meta.Fields)-1]

		if isPK {
			meta.CompositePK = append(meta.CompositePK, PKMeta{Column: dbTag, Index: i, Kind: field.Type.Kind()})
			if !meta.HasPK {
				meta.PK = PKMeta{Column: dbTag, Index: i, Kind: field.Type.Kind()}
				meta.HasPK = true
			}
		}
		if isVersion && meta.VersionFieldIndex < 0 {
			// Cache the index of the version field for the hot Update / Save
			// paths. We store the position within meta.Fields (not the
			// reflect index) so callers can do meta.Fields[idx] directly.
			meta.VersionFieldIndex = len(meta.Fields) - 1
		}
	}

	if len(meta.CompositePK) > 1 {
		meta.HasCompositePK = true
	}

	return meta
}

// Pluralize applies simple English pluralization rules.
func Pluralize(s string) string {
	if strings.HasSuffix(s, "s") || strings.HasSuffix(s, "x") ||
		strings.HasSuffix(s, "ch") || strings.HasSuffix(s, "sh") {
		return s + "es"
	}
	if strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(s[len(s)-2]) {
		return s[:len(s)-1] + "ies"
	}
	return s + "s"
}

func isVowel(c byte) bool {
	return c == 'a' || c == 'e' || c == 'i' || c == 'o' || c == 'u' ||
		c == 'A' || c == 'E' || c == 'I' || c == 'O' || c == 'U'
}

// ToSnakeCase converts CamelCase to snake_case, intelligently handling acronyms.
func ToSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := s[i-1]
			// Add underscore if transitioning from lower to upper,
			// or if transitioning from upper to lower (end of acronym).
			if (prev >= 'a' && prev <= 'z') || (i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z') {
				result.WriteByte('_')
			}
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}

// ColumnFromDBTag returns just the column-name portion of a db tag, stripping
// any sizing options (e.g. "name,size=512" → "name"). Tags without a comma
// are returned unchanged. Used by hot paths in package quark that read the
// raw struct tag and need to feed identifiers to the SQL guard.
func ColumnFromDBTag(tag string) string {
	if i := strings.IndexByte(tag, ','); i >= 0 {
		return strings.TrimSpace(tag[:i])
	}
	return tag
}

// parseDBTag splits a db tag like "name,size=512" into the column name and
// optional SQL-type sizing options. Unknown options are ignored (forward-
// compatible with custom-type-mapper extensions). Numeric values that fail
// to parse are skipped silently — the field's mapper falls back to dialect
// defaults — rather than crashing schema computation.
func parseDBTag(tag string) (col string, size, precision, scale int) {
	if tag == "" {
		return "", 0, 0, 0
	}
	parts := strings.Split(tag, ",")
	col = strings.TrimSpace(parts[0])
	for _, opt := range parts[1:] {
		opt = strings.TrimSpace(opt)
		eq := strings.IndexByte(opt, '=')
		if eq <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(opt[:eq]))
		val := strings.TrimSpace(opt[eq+1:])
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			continue
		}
		switch key {
		case "size":
			size = n
		case "precision":
			precision = n
		case "scale":
			scale = n
		}
	}
	return col, size, precision, scale
}

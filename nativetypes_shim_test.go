package quark

import (
	"reflect"

	"github.com/jcsvwinston/quark/internal/migrate"
)

func migrateSQLType(dialect string, t reflect.Type) string {
	return migrate.SQLTypeWithOpts(dialect, t, migrate.TypeOptions{})
}

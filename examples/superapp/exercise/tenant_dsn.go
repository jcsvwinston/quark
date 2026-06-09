package exercise

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/jcsvwinston/quark/examples/superapp/control"
)

// tenantDBDSN reescribe el DSN base del motor para apuntar a la base de datos
// dbName, manteniendo credenciales/host/params. Es la pieza por-motor de la
// estrategia DatabasePerTenant: el factory del router abre un *Client por tenant
// con el DSN que esto devuelve. Funciones puras (testeadas en tenant_dsn_test.go
// sin motor). Oracle no está: aprovisionar una database por tenant ahí es un PDB
// (ver FeatDBPerTenantProvision en control/capability.go).
func tenantDBDSN(e control.Engine, baseDSN, dbName string) (string, error) {
	switch e {
	case control.SQLite:
		// El DSN de SQLite es una ruta de fichero: una base por tenant es un
		// fichero por tenant, derivado del fichero base.
		return baseDSN + "." + dbName, nil

	case control.Postgres:
		u, err := url.Parse(baseDSN)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			return "", fmt.Errorf("DSN Postgres URL-form requerido, obtuve %q", baseDSN)
		}
		u.Path = "/" + dbName
		return u.String(), nil

	case control.MySQL, control.MariaDB:
		// Formato go-sql-driver: [user[:pass]@][net[(addr)]]/dbname[?params].
		// Se ancla en el ")/" que cierra net(addr) — así una password con '?' o
		// '/' (posible en un SUPERAPP_DSN_* externo) no rompe el split; el paquete
		// no importa el driver a propósito (los drivers van blank-imported en los
		// binarios de test), por eso no se usa mysql.ParseDSN.
		prefix, tail := "", baseDSN
		if p := strings.LastIndex(baseDSN, ")/"); p >= 0 {
			prefix, tail = baseDSN[:p+2], baseDSN[p+2:]
		} else if slash := strings.LastIndexByte(baseDSN, '/'); slash >= 0 {
			// Forma sin net(addr) (p.ej. "/dbname"): split en la última '/'.
			prefix, tail = baseDSN[:slash+1], baseDSN[slash+1:]
		} else {
			return "", fmt.Errorf("DSN MySQL sin '/dbname', obtuve %q", baseDSN)
		}
		params := ""
		if q := strings.IndexByte(tail, '?'); q >= 0 {
			params = tail[q:]
		}
		return prefix + dbName + params, nil

	case control.MSSQL:
		u, err := url.Parse(baseDSN)
		if err != nil || u.Scheme != "sqlserver" {
			return "", fmt.Errorf("DSN MSSQL URL-form requerido, obtuve %q", baseDSN)
		}
		q := u.Query()
		q.Set("database", dbName)
		u.RawQuery = q.Encode()
		return u.String(), nil

	default:
		return "", fmt.Errorf("tenantDBDSN: motor %q sin rewriter (ver FeatDBPerTenantProvision)", e)
	}
}

// createTenantDBSQL devuelve el DDL para crear la base dbName en el motor. El
// nombre viene de constantes del exerciser (no input de usuario); aún así casa
// con validTenantID (^[a-z0-9_-]+$) por construcción.
func createTenantDBSQL(e control.Engine, dbName string) (string, error) {
	switch e {
	case control.Postgres:
		// PG no soporta IF NOT EXISTS en CREATE DATABASE; el cleanup de entrada
		// del exerciser garantiza que no existe.
		return `CREATE DATABASE ` + dbName, nil
	case control.MySQL, control.MariaDB:
		return `CREATE DATABASE IF NOT EXISTS ` + dbName, nil
	case control.MSSQL:
		return `IF DB_ID('` + dbName + `') IS NULL CREATE DATABASE ` + dbName, nil
	default:
		return "", fmt.Errorf("createTenantDBSQL: motor %q no aprovisiona databases", e)
	}
}

// dropTenantDBSQL devuelve el DDL para eliminar la base dbName. Las conexiones
// del tenant deben estar cerradas antes (PG/MSSQL rechazan el DROP con sesiones
// vivas) — el exerciser ordena sus defers para garantizarlo.
func dropTenantDBSQL(e control.Engine, dbName string) (string, error) {
	switch e {
	case control.Postgres, control.MySQL, control.MariaDB, control.MSSQL:
		return `DROP DATABASE IF EXISTS ` + dbName, nil
	default:
		return "", fmt.Errorf("dropTenantDBSQL: motor %q no aprovisiona databases", e)
	}
}

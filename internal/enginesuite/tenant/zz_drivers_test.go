// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package tenant

// The PostgreSQL driver module: it registers the pgx driver, and the listener
// the RLS install path uses. The library's module cannot import it (the
// module imports the library), which is why these tests live here.
import _ "github.com/jcsvwinston/quark/drivers/postgres"

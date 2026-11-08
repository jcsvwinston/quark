// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package render

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Markers that delimit the generated static example inside
// website/docs/playground.mdx. TestStaticExampleMatchesTheDocs rewrites what
// is between them with -update and fails when it differs otherwise.
const (
	StaticBegin = "{/* playground-static-example:begin */}"
	StaticEnd   = "{/* playground-static-example:end */}"
)

// DisplayName is how the documentation names a dialect.
func DisplayName(dialect string) string {
	switch dialect {
	case "postgres":
		return "PostgreSQL"
	case "mysql":
		return "MySQL"
	case "mariadb":
		return "MariaDB"
	case "sqlite":
		return "SQLite"
	case "mssql":
		return "SQL Server"
	case "oracle":
		return "Oracle"
	}
	return dialect
}

// StaticExampleMarkdown renders a Result as the Markdown the documentation
// page shows before the live builder is loaded: the Go chain, one table
// row per dialect, and the bound arguments. The SQL goes in a double-backtick
// code span because MySQL and MariaDB quote identifiers with backticks.
func StaticExampleMarkdown(res Result) string {
	var b strings.Builder
	b.WriteString("```go\n")
	b.WriteString(res.Go)
	b.WriteString("\n```\n\n")
	b.WriteString("| Dialect | SQL |\n|---|---|\n")
	var args []any
	for i, d := range res.Dialects {
		fmt.Fprintf(&b, "| %s | `` %s `` |\n", DisplayName(d.Name), d.SQL)
		if i == 0 {
			args = d.Args
		}
	}
	b.WriteString("\nArguments bound, in placeholder order: ")
	if len(args) == 0 {
		b.WriteString("none.\n")
		return b.String()
	}
	parts := make([]string, len(args))
	for i, a := range args {
		j, _ := json.Marshal(a)
		parts[i] = "`" + string(j) + "`"
	}
	b.WriteString(strings.Join(parts, ", "))
	b.WriteString(".\n")
	return b.String()
}

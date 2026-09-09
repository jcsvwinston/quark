// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package exercise

// Quark carries no driver's error types (ADR-0023). These tests open
// databases, so the test binary links them the way an application would —
// through the driver modules, which register the database/sql driver and the
// error classifier in one import.
import (
	_ "github.com/jcsvwinston/quark/drivers/mssql"
	_ "github.com/jcsvwinston/quark/drivers/mysql"
	_ "github.com/jcsvwinston/quark/drivers/oracle"
	_ "github.com/jcsvwinston/quark/drivers/postgres"
	_ "github.com/jcsvwinston/quark/drivers/sqlite"
)

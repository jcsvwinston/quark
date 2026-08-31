// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package oracle

import (
	"database/sql"
	"slices"
	"testing"

	goora "github.com/sijms/go-ora/v2/network"

	"github.com/jcsvwinston/quark/internal/driverclassify"
	"github.com/jcsvwinston/quark/quarkdriver"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

func TestConformance(t *testing.T) {
	drivertest.Verify(t, drivertest.Case{
		Engine: "oracle",
		Classifier: quarkdriver.Classifier{
			UniqueViolation: driverclassify.OracleUniqueViolation,
			Deadlock:        driverclassify.OracleDeadlock,
			TransientConn:   driverclassify.OracleTransientConn,
		},
		Unique:   &goora.OracleError{ErrCode: 1},  // ORA-00001
		Deadlock: &goora.OracleError{ErrCode: 60}, // ORA-00060
		Neither: []error{
			&goora.OracleError{ErrCode: 2291}, // parent key not found
			&goora.OracleError{ErrCode: 1400}, // cannot insert NULL
		},
	})
}

func TestRegistersTheSQLDriver(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "oracle") {
		t.Errorf("importing this module must register the \"oracle\" driver; registered: %v", sql.Drivers())
	}
}

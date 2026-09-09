// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktest_test

// These tests open a real SQLite database, so the test binary has to link a
// driver. It links the bare pure-Go one rather than the Quark driver module
// an application would import: the driver modules import Quark, and this test
// binary is inside Quark's module, so the requirement would be circular
// (ADR-0023).
//
// What that leaves out is the error classifier the module also registers.
// Nothing here asserts on classification — the predicates are covered by each
// driver module's own conformance test and, against every engine at once, by
// the engine-suite module (ADR-0024), which links the modules properly.
import _ "modernc.org/sqlite"

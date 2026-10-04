// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package errs is the half of the fixture driver that knows its engine's
// errors. It registers through quarkdriver alone, so the bench can ask the
// build graph whether that half needs package quark.
package errs

import (
	"errors"

	"github.com/jcsvwinston/quark/quarkdriver"

	moderncsqlite "modernc.org/sqlite"
)

// Engine is the name the fixture driver registers everywhere: database/sql,
// the dialect registry and the classifier registry.
const Engine = "extsql"

// Classifier recognises the engine's unique and primary-key violations by
// their extended result codes, through errors.As.
var Classifier = quarkdriver.Classifier{
	UniqueViolation: func(err error) bool {
		var e *moderncsqlite.Error
		if errors.As(err, &e) {
			return e.Code() == 2067 || e.Code() == 1555
		}
		return false
	},
	Deadlock:      func(error) bool { return false },
	TransientConn: func(error) bool { return false },
}

func init() { quarkdriver.MustRegister(Engine, Classifier) }

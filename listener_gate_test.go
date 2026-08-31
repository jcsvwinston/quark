// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// TestCreateListener_NonPGReturnsErrDialectNotSupported verifies the
// inbound LISTEN/NOTIFY listener is PostgreSQL-only: every other dialect
// returns ErrDialectNotSupported (ADR-0019). Runs on SQLite, no
// integration needed.
func TestCreateListener_NonPGReturnsErrDialectNotSupported(t *testing.T) {
	t.Parallel()
	c, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	listener, err := quark.NewListenerFactory(c).CreateListener()
	if !errors.Is(err, quark.ErrDialectNotSupported) {
		t.Fatalf("CreateListener on sqlite: got err=%v, want ErrDialectNotSupported", err)
	}
	if listener != nil {
		t.Fatalf("CreateListener on sqlite returned a non-nil listener: %#v", listener)
	}
}

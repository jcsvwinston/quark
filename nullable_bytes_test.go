// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"bytes"
	"context"
	"testing"
)

type nbProfile struct {
	ID     int64            `db:"id" pk:"true"`
	Avatar Nullable[[]byte] `db:"avatar"`
}

// TestNullableBytesRoundTrip inserts both a NULL and a non-NULL
// Nullable[[]byte] and reads them back, exercising the bindColumnArg path the
// BB-6 fix touches. On SQLite the NULL case already worked; this guards the
// round-trip contract and runs as the fast in-package companion to the F3
// bug-bash phase, which exercises the same insert on MSSQL.
func TestNullableBytesRoundTrip(t *testing.T) {
	ctx := context.Background()
	client, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()
	if err := client.Migrate(ctx, &nbProfile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	null := &nbProfile{Avatar: Nullable[[]byte]{}}
	if err := For[nbProfile](ctx, client).Create(null); err != nil {
		t.Fatalf("create null avatar: %v", err)
	}
	set := &nbProfile{Avatar: Nullable[[]byte]{V: []byte{0xDE, 0xAD}, Valid: true}}
	if err := For[nbProfile](ctx, client).Create(set); err != nil {
		t.Fatalf("create set avatar: %v", err)
	}

	gotNull, err := For[nbProfile](ctx, client).Find(null.ID)
	if err != nil {
		t.Fatalf("find null: %v", err)
	}
	if gotNull.Avatar.Valid {
		t.Errorf("NULL avatar came back Valid=true (%v)", gotNull.Avatar.V)
	}
	gotSet, err := For[nbProfile](ctx, client).Find(set.ID)
	if err != nil {
		t.Fatalf("find set: %v", err)
	}
	if !gotSet.Avatar.Valid || !bytes.Equal(gotSet.Avatar.V, []byte{0xDE, 0xAD}) {
		t.Errorf("set avatar round-trip = %+v, want [0xDE 0xAD]", gotSet.Avatar)
	}
}

// TestNullBytesArg pins the BB-6 fix: an invalid Nullable[[]byte] must bind as
// a typed nil []byte (a binary NULL on every driver), not as the untyped nil
// its Valuer returns — which go-mssqldb encodes as nvarchar and SQL Server
// then refuses to insert into a varbinary column.
func TestNullBytesArg(t *testing.T) {
	t.Run("InvalidNullableBytesBecomesTypedNil", func(t *testing.T) {
		got := nullBytesArg(Nullable[[]byte]{Valid: false})
		b, ok := got.([]byte)
		if !ok {
			t.Fatalf("got %T, want []byte", got)
		}
		if b != nil {
			t.Errorf("got %v, want nil []byte", b)
		}
	})

	t.Run("ValidNullableBytesPassesThrough", func(t *testing.T) {
		in := Nullable[[]byte]{V: []byte{0x01, 0x02}, Valid: true}
		got := nullBytesArg(in)
		nb, ok := got.(Nullable[[]byte])
		if !ok {
			t.Fatalf("got %T, want Nullable[[]byte] (valid value must pass through unchanged)", got)
		}
		if !nb.Valid || !bytes.Equal(nb.V, []byte{0x01, 0x02}) {
			t.Errorf("got %+v, want the original valid value", nb)
		}
	})

	t.Run("OtherTypesPassThrough", func(t *testing.T) {
		for _, v := range []any{int64(7), "hello", Nullable[string]{}, []byte{0x09}} {
			if got := nullBytesArg(v); got == nil {
				t.Errorf("nullBytesArg(%v) returned nil; non-binary values must pass through", v)
			}
		}
	})
}

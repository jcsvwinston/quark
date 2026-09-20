// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// KeysetPage is one page of a keyset (seek) pagination: the rows, and the
// opaque token that names where they stopped. Hand Next back to
// [Query.PaginateAfter] to get the rows after them; HasMore says whether
// there are any. Unlike [Page], it carries no total and no page number: a
// keyset page costs one statement and the same time whether it is the first
// or the thousandth, because the server seeks to the last row read instead of
// walking past every page before it (A8 S9).
type KeysetPage[T any] struct {
	Items   []T
	Next    string
	HasMore bool
}

// PaginateAfter returns the first pageSize rows after the position named by
// after — "" for the first page — in the query's ORDER BY, and the token of
// the last row returned. The ordering decides the seek: for ORDER BY a ASC,
// b DESC the continuation is `a > ?  OR (a = ? AND b < ?)`, expanded rather
// than written as a row comparison because SQL Server and Oracle have none.
// The primary key is appended to the ordering when it is not already there,
// so the order is total and no row is skipped or repeated between pages.
//
// Every ordering column has to be a column of the model: the token is built
// from the last row's values, and an expression has none to read back. The
// token is opaque and versioned by shape — a token minted under a different
// ORDER BY is refused with ErrInvalidQuery rather than seeking to the wrong
// place.
func (q *Query[T]) PaginateAfter(pageSize int, after string) (*KeysetPage[T], error) {
	if q.err != nil {
		return nil, q.err
	}
	if q.client == nil {
		return nil, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	if q.meta == nil {
		return nil, fmt.Errorf("%w: PaginateAfter needs a model with metadata", ErrInvalidQuery)
	}
	if q.pk.Column == "" {
		return nil, fmt.Errorf("%w: PaginateAfter needs a primary key to make the order total", ErrInvalidQuery)
	}

	// The ordering, made total with the primary key.
	orders := append([]order(nil), q.orderBy...)
	hasPK := false
	for _, o := range orders {
		if strings.EqualFold(o.column, q.pk.Column) {
			hasPK = true
		}
	}
	if !hasPK {
		orders = append(orders, order{column: q.pk.Column})
	}
	fields := make([]*FieldMeta, len(orders))
	for i, o := range orders {
		fm, ok := q.meta.FieldByCol[strings.ToLower(o.column)]
		if !ok {
			return nil, fmt.Errorf("%w: PaginateAfter: ORDER BY column %q is not a column of %s, so no token can be read from it",
				ErrInvalidQuery, o.column, q.table)
		}
		fields[i] = fm
	}

	pq := q.clone()
	if !hasPK {
		pq.orderBy = ownedAppend(pq.orderBy, order{column: q.pk.Column})
	}
	if after != "" {
		values, err := decodeKeysetToken(after, orders)
		if err != nil {
			return nil, err
		}
		pq = pq.WhereExpr(keysetPredicate(orders, values))
	}
	pq.limit = pageSize + 1
	pq.hasLimit = true
	pq.offset = 0

	items, err := pq.List()
	if err != nil {
		return nil, err
	}
	page := &KeysetPage[T]{}
	if len(items) > pageSize {
		page.HasMore = true
		items = items[:pageSize]
	}
	page.Items = items
	if len(items) > 0 {
		last := reflect.ValueOf(items[len(items)-1])
		values := make([]any, len(fields))
		for i, fm := range fields {
			values[i] = last.Field(fm.Index).Interface()
		}
		tok, err := encodeKeysetToken(orders, values)
		if err != nil {
			return nil, err
		}
		page.Next = tok
	}
	return page, nil
}

// keysetPredicate renders the seek for the ordering: rows strictly after
// the position, column by column — the row-value comparison (a, b) > (?, ?)
// spelled out, because not every engine has it.
func keysetPredicate(orders []order, values []any) Expr {
	var branches []Expr
	for i, o := range orders {
		var parts []Expr
		for j := 0; j < i; j++ {
			parts = append(parts, Eq(Col(orders[j].column), Lit(values[j])))
		}
		if o.desc {
			parts = append(parts, Lt(Col(o.column), Lit(values[i])))
		} else {
			parts = append(parts, Gt(Col(o.column), Lit(values[i])))
		}
		branches = append(branches, And(parts...))
	}
	return Or(branches...)
}

// keysetToken is what the opaque string carries: the ordering it was minted
// under, so a token from another ORDER BY is refused, and the values of the
// last row in that order.
type keysetToken struct {
	Order  []string `json:"o"`
	Values []any    `json:"v"`
}

func encodeKeysetToken(orders []order, values []any) (string, error) {
	tok := keysetToken{Values: values}
	for _, o := range orders {
		tok.Order = append(tok.Order, orderSpec(o))
	}
	b, err := json.Marshal(tok)
	if err != nil {
		return "", fmt.Errorf("PaginateAfter: encode token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeKeysetToken(s string, orders []order) ([]any, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: PaginateAfter: the token is not one this query minted", ErrInvalidQuery)
	}
	var tok keysetToken
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, fmt.Errorf("%w: PaginateAfter: the token is not one this query minted", ErrInvalidQuery)
	}
	if len(tok.Order) != len(orders) || len(tok.Values) != len(orders) {
		return nil, fmt.Errorf("%w: PaginateAfter: the token was minted under another ORDER BY", ErrInvalidQuery)
	}
	for i, o := range orders {
		if tok.Order[i] != orderSpec(o) {
			return nil, fmt.Errorf("%w: PaginateAfter: the token was minted under another ORDER BY (%s, not %s)",
				ErrInvalidQuery, tok.Order[i], orderSpec(o))
		}
	}
	return tok.Values, nil
}

func orderSpec(o order) string {
	if o.desc {
		return o.column + " DESC"
	}
	return o.column + " ASC"
}

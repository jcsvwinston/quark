// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

// Typed column accessors (F6-4). `quark gen` emits, per model, a
// `<Model>Columns` value whose fields are [TypedColumn] / [TypedStringColumn]
// handles built from the model's `db` tags. They let you write WHERE
// conditions without magic column strings and with compile-time checking of
// both the column (a field typo does not compile) and the bound value (a
// wrong-typed value does not compile):
//
//	users, err := quark.For[User](ctx, c).
//		WhereP(UserColumns.Email.Eq("a@b.com"), UserColumns.Age.Gte(18)).
//		List()
//
// This is pure compile-time sugar layered on the same runtime as the string
// API (ADR-0014): [Query.WhereP] lowers each [Predicate] to the identical
// internal condition that Where(column, operator, value) produces, so the two
// are interchangeable and the string form (`Where("email", "=", v)`) stays
// valid. Without codegen the accessors simply do not exist; nothing else in
// the runtime changes. OR / grouping are not offered here — keep those on the
// string Where / Or API.

// Predicate is a single typed WHERE condition produced by a [TypedColumn]
// accessor. It is opaque: build it through a TypedColumn method (Eq, Gt, In,
// …) and pass it to [Query.WhereP]. Because the only construction path is a
// generated column handle, the column identifier never originates from
// caller-supplied input.
type Predicate struct {
	column   string
	operator string
	value    any
}

// toCondition lowers the predicate to the builder's internal condition with
// the given boolean connective ("AND" / "OR").
func (p Predicate) toCondition(logic string) condition {
	return condition{
		column:   p.column,
		operator: p.operator,
		value:    p.value,
		logic:    logic,
	}
}

// TypedColumn is a typed handle to one model column, parameterised by the Go
// type of the underlying field. Generated code constructs it via
// [NewTypedColumn]; its methods produce [Predicate]s whose value argument is
// typed T, so passing a value of the wrong type for the column is a compile
// error.
type TypedColumn[T any] struct {
	name string
}

// NewTypedColumn builds a typed column handle for the given SQL column name.
// It is intended to be called only from generated code, where name is the
// model's `db` tag column.
func NewTypedColumn[T any](name string) TypedColumn[T] { return TypedColumn[T]{name: name} }

// Name returns the SQL column name the handle refers to.
func (c TypedColumn[T]) Name() string { return c.name }

// Eq builds `column = value`.
func (c TypedColumn[T]) Eq(v T) Predicate { return Predicate{c.name, "=", v} }

// Neq builds `column != value`.
func (c TypedColumn[T]) Neq(v T) Predicate { return Predicate{c.name, "!=", v} }

// Gt builds `column > value`.
func (c TypedColumn[T]) Gt(v T) Predicate { return Predicate{c.name, ">", v} }

// Gte builds `column >= value`.
func (c TypedColumn[T]) Gte(v T) Predicate { return Predicate{c.name, ">=", v} }

// Lt builds `column < value`.
func (c TypedColumn[T]) Lt(v T) Predicate { return Predicate{c.name, "<", v} }

// Lte builds `column <= value`.
func (c TypedColumn[T]) Lte(v T) Predicate { return Predicate{c.name, "<=", v} }

// In builds `column IN (values...)`. Pass at least one value: with none it
// lowers to the same empty-IN condition as the string WhereIn — which SQLite
// treats as matching nothing but PostgreSQL, Oracle, and SQL Server reject as
// invalid SQL. (WhereP is a faithful lowering of the string API, so it does
// not paper over that engine difference.)
func (c TypedColumn[T]) In(values ...T) Predicate {
	return Predicate{c.name, "IN", typedToAny(values)}
}

// NotIn builds `column NOT IN (values...)`. Pass at least one value — see In
// for the empty-list caveat.
func (c TypedColumn[T]) NotIn(values ...T) Predicate {
	return Predicate{c.name, "NOT IN", typedToAny(values)}
}

// Between builds `column BETWEEN lo AND hi`.
func (c TypedColumn[T]) Between(lo, hi T) Predicate {
	return Predicate{c.name, "BETWEEN", []any{lo, hi}}
}

// IsNull builds `column IS NULL`.
func (c TypedColumn[T]) IsNull() Predicate { return Predicate{c.name, "IS NULL", nil} }

// IsNotNull builds `column IS NOT NULL`.
func (c TypedColumn[T]) IsNotNull() Predicate { return Predicate{c.name, "IS NOT NULL", nil} }

// typedToAny widens a typed slice to []any for the IN / NOT IN bind list.
func typedToAny[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// TypedStringColumn is a [TypedColumn][string] that additionally offers the
// text-only operators LIKE / NOT LIKE. The generator uses it for string-typed
// fields so pattern matching is offered only where it is meaningful, while
// every TypedColumn[string] method (Eq, In, …) remains available via
// embedding.
type TypedStringColumn struct {
	TypedColumn[string]
}

// NewTypedStringColumn builds a typed string-column handle. Intended to be
// called only from generated code.
func NewTypedStringColumn(name string) TypedStringColumn {
	return TypedStringColumn{TypedColumn[string]{name: name}}
}

// Like builds `column LIKE pattern`.
func (c TypedStringColumn) Like(pattern string) Predicate { return Predicate{c.name, "LIKE", pattern} }

// NotLike builds `column NOT LIKE pattern`.
func (c TypedStringColumn) NotLike(pattern string) Predicate {
	return Predicate{c.name, "NOT LIKE", pattern}
}

// WhereP appends one or more typed predicates (built from generated column
// accessors) to the query, combined with AND. It is the compile-time-safe
// counterpart to [Query.Where]; the two may be mixed freely on the same
// query. Returns a clone, like every other builder method.
func (q *Query[T]) WhereP(preds ...Predicate) *Query[T] {
	c := q.clone()
	// Lower all predicates first, then append once: a single ownedAppend
	// reallocates the (possibly shared) where slice exactly once, rather than
	// re-clamping and reallocating per predicate.
	conds := make([]condition, len(preds))
	for i, p := range preds {
		conds[i] = p.toCondition("AND")
	}
	c.where = ownedAppend(c.where, conds...)
	return c
}

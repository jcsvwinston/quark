// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// This file is A8 S6: the values an application already has — a raw Go slice
// or map, an IP address, a range — stored in the column type each engine has
// for them. PostgreSQL has array, inet and range types and the operators that
// go with them; every other engine gets a JSON-backed or text column and the
// same round trip, and the PostgreSQL-only operators are known to the builder
// and refused by ENGINE there (ErrUnsupportedFeature naming the dialect),
// never silently rewritten.

// Range is a bounded interval of T, the shape of PostgreSQL's range types.
// Bounds is the two-character bound spec — "[)" (the zero value, and
// PostgreSQL's canonical form), "[]", "(]" or "()" — and Empty marks the
// empty range.
//
// On PostgreSQL a Range[time.Time] column is TSTZRANGE, Range[int64] is
// INT8RANGE, Range[int32] INT4RANGE and Range[float64] NUMRANGE, and the
// value travels in the engine's range literal; elsewhere the column is the
// dialect's JSON type and the value a JSON object. Containment is asked with
// the `@>` / `<@` operators, on PostgreSQL only.
type Range[T any] struct {
	Lower  T
	Upper  T
	Bounds string
	Empty  bool
}

func (r Range[T]) bounds() string {
	if r.Bounds == "" {
		return "[)"
	}
	return r.Bounds
}

type rangeJSON[T any] struct {
	Lower  T      `json:"lower"`
	Upper  T      `json:"upper"`
	Bounds string `json:"bounds"`
	Empty  bool   `json:"empty,omitempty"`
}

// Value implements driver.Valuer as JSON, the form every engine but
// PostgreSQL stores; the bind path substitutes the range literal there.
func (r Range[T]) Value() (driver.Value, error) {
	b, err := json.Marshal(rangeJSON[T]{Lower: r.Lower, Upper: r.Upper, Bounds: r.bounds(), Empty: r.Empty})
	if err != nil {
		return nil, fmt.Errorf("Range.Value: %w", err)
	}
	return string(b), nil
}

// PGLiteral renders the PostgreSQL range literal: `[lower,upper)`.
func (r Range[T]) PGLiteral() (string, error) {
	if r.Empty {
		return "empty", nil
	}
	lo, err := formatRangeBound(r.Lower)
	if err != nil {
		return "", err
	}
	hi, err := formatRangeBound(r.Upper)
	if err != nil {
		return "", err
	}
	b := r.bounds()
	return b[:1] + lo + "," + hi + b[1:], nil
}

// Scan accepts the JSON object the non-PostgreSQL engines store and the
// range literal PostgreSQL returns.
func (r *Range[T]) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*r = Range[T]{}
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("Range.Scan: unsupported source type %T", src)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*r = Range[T]{}
		return nil
	}
	if s == "empty" {
		*r = Range[T]{Empty: true}
		return nil
	}
	if s[0] == '{' {
		var j rangeJSON[T]
		if err := json.Unmarshal([]byte(s), &j); err != nil {
			return fmt.Errorf("Range.Scan: %w", err)
		}
		*r = Range[T]{Lower: j.Lower, Upper: j.Upper, Bounds: j.Bounds, Empty: j.Empty}
		return nil
	}
	if (s[0] != '[' && s[0] != '(') || len(s) < 3 {
		return fmt.Errorf("Range.Scan: %q is neither a JSON object nor a range literal", s)
	}
	body := s[1 : len(s)-1]
	lo, hi, ok := splitRangeBody(body)
	if !ok {
		return fmt.Errorf("Range.Scan: cannot split the range literal %q", s)
	}
	var out Range[T]
	out.Bounds = s[:1] + s[len(s)-1:]
	if err := parseRangeBound(&out.Lower, lo); err != nil {
		return fmt.Errorf("Range.Scan: lower bound: %w", err)
	}
	if err := parseRangeBound(&out.Upper, hi); err != nil {
		return fmt.Errorf("Range.Scan: upper bound: %w", err)
	}
	*r = out
	return nil
}

// splitRangeBody splits `lo,hi` at the top-level comma, honouring the
// double quotes PostgreSQL puts around a bound that contains one.
func splitRangeBody(body string) (lo, hi string, ok bool) {
	inQuote := false
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				return strings.Trim(body[:i], `"`), strings.Trim(body[i+1:], `"`), true
			}
		}
	}
	return "", "", false
}

// formatRangeBound renders one bound for the PostgreSQL literal; an
// unbounded side (the zero time) renders empty, which PostgreSQL reads as
// infinity.
func formatRangeBound(v any) (string, error) {
	switch b := v.(type) {
	case time.Time:
		if b.IsZero() {
			return "", nil
		}
		return `"` + b.UTC().Format(time.RFC3339Nano) + `"`, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", b), nil
	case float32, float64:
		return fmt.Sprintf("%v", b), nil
	case string:
		return `"` + strings.ReplaceAll(b, `"`, `\"`) + `"`, nil
	}
	return "", fmt.Errorf("Range: no PostgreSQL literal for a bound of type %T", v)
}

// parseRangeBound reads one bound of a PostgreSQL range literal into dst.
func parseRangeBound(dst any, text string) error {
	text = strings.TrimSpace(text)
	switch d := dst.(type) {
	case *time.Time:
		if text == "" {
			*d = time.Time{}
			return nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
			if t, err := time.Parse(layout, text); err == nil {
				*d = t
				return nil
			}
		}
		return fmt.Errorf("cannot parse %q as a timestamp", text)
	case *int64:
		n, err := strconv.ParseInt(text, 10, 64)
		*d = n
		return err
	case *int:
		n, err := strconv.ParseInt(text, 10, 64)
		*d = int(n)
		return err
	case *int32:
		n, err := strconv.ParseInt(text, 10, 32)
		*d = int32(n)
		return err
	case *float64:
		f, err := strconv.ParseFloat(text, 64)
		*d = f
		return err
	case *string:
		*d = text
		return nil
	}
	return json.Unmarshal([]byte(text), dst)
}

// pgLiteraler is implemented by the values that have a PostgreSQL literal
// of their own beside their portable JSON form.
type pgLiteraler interface {
	PGLiteral() (string, error)
}

// nativeBind converts a Go value the standard library cannot bind — a raw
// slice or map, a net.IP, a Range — into what the engine stores: the
// PostgreSQL literal when the engine is PostgreSQL and the value is a scalar
// slice or a Range, JSON text otherwise, and the textual address for an IP.
// Anything else is returned untouched.
func (q *BaseQuery) nativeBind(val any) any {
	if val == nil {
		return nil
	}
	pg := q.dialect != nil && q.dialect.Name() == "postgres"
	switch v := val.(type) {
	case net.IP:
		if len(v) == 0 {
			return nil
		}
		return v.String()
	case *net.IP:
		if v == nil || len(*v) == 0 {
			return nil
		}
		return v.String()
	case []byte:
		return val
	}
	if pg {
		if lit, ok := val.(pgLiteraler); ok {
			s, err := lit.PGLiteral()
			if err != nil {
				return val // the engine reports the shape the literal could not take
			}
			return s
		}
	}
	if _, ok := val.(driver.Valuer); ok {
		return val
	}
	rv := reflect.ValueOf(val)
	switch rv.Kind() {
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return val
		}
		if pg && isScalarKind(rv.Type().Elem().Kind()) {
			return pgArrayLiteral(rv)
		}
		return jsonText(val)
	case reflect.Map:
		return jsonText(val)
	}
	return val
}

func jsonText(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	return string(b)
}

func isScalarKind(k reflect.Kind) bool {
	switch k {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// pgArrayLiteral renders a scalar slice as a PostgreSQL array literal:
// `{"a","b"}`, `{1,2}`, `{t,f}`. Strings are always quoted, with `\` and
// `"` escaped, so an element that is empty or contains a comma survives.
func pgArrayLiteral(rv reflect.Value) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		e := rv.Index(i)
		switch e.Kind() {
		case reflect.String:
			s := e.String()
			s = strings.ReplaceAll(s, `\`, `\\`)
			s = strings.ReplaceAll(s, `"`, `\"`)
			b.WriteString(`"` + s + `"`)
		case reflect.Bool:
			if e.Bool() {
				b.WriteString("t")
			} else {
				b.WriteString("f")
			}
		default:
			b.WriteString(fmt.Sprintf("%v", e.Interface()))
		}
	}
	b.WriteByte('}')
	return b.String()
}

// parsePGArray reads a one-dimensional PostgreSQL array literal into its
// elements; a NULL element comes back as ok=false in nulls.
func parsePGArray(s string) (elems []string, nulls []bool, err error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, nil, fmt.Errorf("not an array literal: %q", s)
	}
	body := s[1 : len(s)-1]
	if body == "" {
		return []string{}, []bool{}, nil
	}
	var cur strings.Builder
	inQuote, quoted := false, false
	flush := func() {
		v := cur.String()
		if !quoted && v == "NULL" {
			elems = append(elems, "")
			nulls = append(nulls, true)
		} else {
			elems = append(elems, v)
			nulls = append(nulls, false)
		}
		cur.Reset()
		quoted = false
	}
	for i := 0; i < len(body); i++ {
		ch := body[i]
		switch {
		case inQuote && ch == '\\' && i+1 < len(body):
			i++
			cur.WriteByte(body[i])
		case ch == '"':
			inQuote = !inQuote
			quoted = true
		case ch == ',' && !inQuote:
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	return elems, nulls, nil
}

// sliceScanner scans a column into a raw Go slice: a PostgreSQL array
// literal or a JSON array, whichever the engine returns.
type sliceScanner struct{ dest reflect.Value }

func (s sliceScanner) Scan(src any) error {
	var text string
	switch v := src.(type) {
	case nil:
		s.dest.Set(reflect.Zero(s.dest.Type()))
		return nil
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("cannot scan %T into %s", src, s.dest.Type())
	}
	text = strings.TrimSpace(text)
	if text == "" {
		s.dest.Set(reflect.Zero(s.dest.Type()))
		return nil
	}
	if text[0] == '{' {
		elems, nulls, err := parsePGArray(text)
		if err != nil {
			return err
		}
		out := reflect.MakeSlice(s.dest.Type(), len(elems), len(elems))
		for i, e := range elems {
			if nulls[i] {
				continue
			}
			if err := setScalar(out.Index(i), e); err != nil {
				return fmt.Errorf("array element %d: %w", i, err)
			}
		}
		s.dest.Set(out)
		return nil
	}
	target := reflect.New(s.dest.Type())
	if err := json.Unmarshal([]byte(text), target.Interface()); err != nil {
		return fmt.Errorf("scan %s: %w", s.dest.Type(), err)
	}
	s.dest.Set(target.Elem())
	return nil
}

func setScalar(dst reflect.Value, text string) error {
	switch dst.Kind() {
	case reflect.String:
		dst.SetString(text)
	case reflect.Bool:
		dst.SetBool(text == "t" || text == "true")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return err
		}
		dst.SetInt(n)
	case reflect.Uint, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return err
		}
		dst.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return err
		}
		dst.SetFloat(f)
	default:
		return fmt.Errorf("no scalar conversion for %s", dst.Kind())
	}
	return nil
}

// jsonScanner scans a JSON column into a raw Go map.
type jsonScanner struct{ dest reflect.Value }

func (s jsonScanner) Scan(src any) error {
	var text []byte
	switch v := src.(type) {
	case nil:
		s.dest.Set(reflect.Zero(s.dest.Type()))
		return nil
	case string:
		text = []byte(v)
	case []byte:
		text = v
	default:
		return fmt.Errorf("cannot scan %T into %s", src, s.dest.Type())
	}
	if len(strings.TrimSpace(string(text))) == 0 {
		s.dest.Set(reflect.Zero(s.dest.Type()))
		return nil
	}
	target := reflect.New(s.dest.Type())
	if err := json.Unmarshal(text, target.Interface()); err != nil {
		return fmt.Errorf("scan %s: %w", s.dest.Type(), err)
	}
	s.dest.Set(target.Elem())
	return nil
}

// ipScanner scans an address column into a net.IP: the text form every
// engine stores now, and the raw 4 or 16 bytes a column written before
// A8 S6 may still hold.
type ipScanner struct{ dest *net.IP }

func (s ipScanner) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*s.dest = nil
	case string:
		return s.parse(v)
	case []byte:
		if len(v) == 4 || len(v) == 16 {
			if ip := net.IP(v); ip.To16() != nil && !looksLikeText(v) {
				*s.dest = append(net.IP(nil), ip...)
				return nil
			}
		}
		return s.parse(string(v))
	default:
		return fmt.Errorf("cannot scan %T into net.IP", src)
	}
	return nil
}

func (s ipScanner) parse(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		*s.dest = nil
		return nil
	}
	if i := strings.IndexByte(text, '/'); i > 0 { // inet may carry a netmask
		text = text[:i]
	}
	ip := net.ParseIP(text)
	if ip == nil {
		return fmt.Errorf("cannot parse %q as an IP address", text)
	}
	*s.dest = ip
	return nil
}

func looksLikeText(b []byte) bool {
	for _, c := range b {
		if c < ' ' || c > '~' {
			return false
		}
	}
	return true
}

var (
	netIPType  = reflect.TypeOf(net.IP{})
	scannerTyp = reflect.TypeOf((*interface{ Scan(any) error })(nil)).Elem()
)

// nativeScanDest returns the scan target for a field whose Go type the
// standard library cannot receive — a raw slice or map, a net.IP — or nil
// when the field needs none. A type with its own Scanner keeps it.
func nativeScanDest(field reflect.Value) any {
	if !field.CanAddr() {
		return nil
	}
	t := field.Type()
	if reflect.PointerTo(t).Implements(scannerTyp) {
		return nil
	}
	if t == netIPType {
		return ipScanner{dest: field.Addr().Interface().(*net.IP)}
	}
	switch t.Kind() {
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		return sliceScanner{dest: field}
	case reflect.Map:
		return jsonScanner{dest: field}
	}
	return nil
}

// pgOnlyOperators are the comparison operators PostgreSQL has for its array,
// range and network types. The guard knows them; the dialect gate below
// refuses them on any other engine, so a query that asks for containment on
// MySQL fails with ErrUnsupportedFeature naming the engine instead of a
// syntax error from the server — or, worse, MySQL reading `<<` as a shift.
var pgOnlyOperators = map[string]bool{
	"@>": true, "<@": true, "&&": true,
	"<<": true, ">>": true, "<<=": true, ">>=": true,
}

// checkOperatorDialect refuses a PostgreSQL-only operator on another engine.
func checkOperatorDialect(d Dialect, op string) error {
	op = strings.TrimSpace(op)
	if !pgOnlyOperators[op] {
		return nil
	}
	if d == nil || d.Name() != "postgres" {
		name := "unknown"
		if d != nil {
			name = d.Name()
		}
		return fmt.Errorf("%w: operator %q is a PostgreSQL operator (array, range and network containment) and this query runs on %s",
			ErrUnsupportedFeature, op, name)
	}
	return nil
}

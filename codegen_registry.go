// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// This file is the F6-1 anchor for the codegen ↔ reflect coexistence
// mechanism decided in ADR-0014. It establishes the *contract* the
// generator and the runtime share, without yet installing any fast path:
//
//   - Generated code (emitted by `quark gen` into the user's package)
//     registers per-model typed implementations here from an init().
//   - The runtime read/write hot paths (scanRow, buildInsert, …) will, in
//     F6-2/F6-3, consult these registries by reflect.Type *before* falling
//     back to reflection. F6-1 does NOT wire that lookup in: with no
//     generated code the registries are empty and every operation uses the
//     existing reflection path, unchanged.
//
// The public API (quark.For[T], Query[T]) is identical with or without
// generated code; the only observable difference, once F6-2/F6-3 land, is
// latency. See ADR-0014 for the rationale and the rejected alternatives.

// GenContractVersion is the version of the code-generation contract: the
// shape of the generated files and the signatures of the RegisterTyped*
// functions below. The generator stamps a matching `//quark:gen vN` header
// into every file it emits.
//
// Bump this on any breaking change to the generated-file shape or the
// registrar signatures. Generated code that registers a different version
// is ignored at lookup time (the operation falls back to reflection), so a
// stale binary never calls into incompatible generated code.
//
// v2 (F6-2): generated files emit a real typed scanner (read path) instead
// of the F6-1 stub. v3 (F6-3a): integer-PK models also emit a real INSERT
// binder. Older files are ignored and fall back to reflection.
const GenContractVersion = 3

// BindMode selects which column set a TypedBinder produces. Insert binds
// every persisted column; Update binds the non-PK columns of a full-row
// update. (Partial updates — buildUpdateMap / UpdateFields — are handled by
// F6-3 and may extend this enum; new values append, never reorder.)
type BindMode int

const (
	// BindInsert requests the columns and args for an INSERT.
	BindInsert BindMode = iota
	// BindUpdate requests the non-PK columns and args for a full UPDATE.
	BindUpdate
)

// TypedScanner scans the current row of rows into dest — a non-nil pointer
// to the model value (*T) — without reflection. It is implemented by
// generated code and registered via RegisterTypedScanner. Not intended for
// hand use.
type TypedScanner func(rows *sql.Rows, dest any) error

// TypedBinder returns the column names and bind arguments for entity — a
// non-nil pointer to the model value (*T) — for the given mode, without
// reflection. Implemented by generated code and registered via
// RegisterTypedBinder. Not intended for hand use.
type TypedBinder func(entity any, mode BindMode) (cols []string, args []any, err error)

// GeneratedMeta carries the bookkeeping a generated file registers
// alongside its scanner/binder: the contract version it was emitted
// against and a hash of the model's shape at generation time. The hash
// lets an optional drift check (CheckGeneratedDrift) warn when a model has
// changed but `quark gen` was not re-run.
type GeneratedMeta struct {
	// ContractVersion is the GenContractVersion the file was generated for.
	ContractVersion int
	// ModelHash is ModelHash(t) computed at generation time.
	ModelHash string
}

var (
	codegenMu     sync.RWMutex
	typedScanners = make(map[reflect.Type]TypedScanner)
	typedBinders  = make(map[reflect.Type]TypedBinder)
	generatedMeta = make(map[reflect.Type]GeneratedMeta)
)

// modelKey normalizes a type to the non-pointer struct type used as the
// registry key, so registration (which keys on the value type) and lookup
// (which may start from *T) agree.
func modelKey(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// RegisterTypedScanner records the generated row scanner for model type t.
// Called from the init() of a generated file; not intended for hand use.
func RegisterTypedScanner(t reflect.Type, fn TypedScanner) {
	codegenMu.Lock()
	defer codegenMu.Unlock()
	typedScanners[modelKey(t)] = fn
}

// RegisterTypedBinder records the generated insert/update binder for model
// type t. Called from the init() of a generated file; not intended for
// hand use.
func RegisterTypedBinder(t reflect.Type, fn TypedBinder) {
	codegenMu.Lock()
	defer codegenMu.Unlock()
	typedBinders[modelKey(t)] = fn
}

// RegisterGeneratedMeta records the contract version and model hash for a
// generated model. Called from the init() of a generated file; not
// intended for hand use.
func RegisterGeneratedMeta(t reflect.Type, meta GeneratedMeta) {
	codegenMu.Lock()
	defer codegenMu.Unlock()
	generatedMeta[modelKey(t)] = meta
}

// generatedCompatible reports whether t has generated metadata registered
// for the current contract version. Generated code from a different
// contract version is treated as absent, so the operation falls back to
// reflection rather than calling into an incompatible implementation.
//
// Callers must hold codegenMu (read or write): it reads generatedMeta
// without taking the lock itself, so it can be used from the lookup helpers
// that already hold the read lock (sync.RWMutex is not reentrant).
func generatedCompatible(t reflect.Type) bool {
	meta, ok := generatedMeta[modelKey(t)]
	return ok && meta.ContractVersion == GenContractVersion
}

// lookupTypedScanner returns the generated scanner for t, but only when the
// registered generated code matches the current contract version. A miss
// (no codegen, or an incompatible version) signals the caller to use the
// reflection path. Wired into scanRow by F6-2; unused in F6-1.
func lookupTypedScanner(t reflect.Type) (TypedScanner, bool) {
	codegenMu.RLock()
	defer codegenMu.RUnlock()
	if !generatedCompatible(t) {
		return nil, false
	}
	fn, ok := typedScanners[modelKey(t)]
	return fn, ok
}

// lookupTypedBinder returns the generated binder for t, subject to the same
// contract-version gate as lookupTypedScanner. Wired into the write path by
// F6-3; unused in F6-1.
func lookupTypedBinder(t reflect.Type) (TypedBinder, bool) {
	codegenMu.RLock()
	defer codegenMu.RUnlock()
	if !generatedCompatible(t) {
		return nil, false
	}
	fn, ok := typedBinders[modelKey(t)]
	return fn, ok
}

// ErrGeneratedStub is returned by the placeholder scanner/binder that F6-1
// generated code registers. F6-1 ships the generation pipeline and the
// registry contract but not the typed fast path itself (that is F6-2 for
// scanning and F6-3 for binding); until then the generated functions are
// inert. They are never reached at runtime in F6-1 because the hot paths do
// not yet consult the registry, and once they do, the contract-version gate
// keeps a v1 stub from being used by a newer runtime.
var ErrGeneratedStub = errors.New("quark: generated code is an F6-1 stub; the typed fast path lands in F6-2/F6-3")

// StubScanner is the inert scanner registered by F6-1 generated code. See
// ErrGeneratedStub.
func StubScanner(*sql.Rows, any) error { return ErrGeneratedStub }

// StubBinder is the inert binder registered by F6-1 generated code. See
// ErrGeneratedStub.
func StubBinder(any, BindMode) ([]string, []any, error) { return nil, nil, ErrGeneratedStub }

// GeneratedBinderRegistered reports whether a compatible generated binder is
// registered for t that actually handles inserts — i.e. a real binder, not
// the stub (which returns ErrGeneratedStub). Intended for tests that want to
// assert the generated write path is exercised rather than reflection.
func GeneratedBinderRegistered(t reflect.Type) bool {
	bind, ok := lookupTypedBinder(t)
	if !ok {
		return false
	}
	_, _, err := bind(reflect.New(modelKey(t)).Interface(), BindInsert)
	return err == nil
}

// CheckGeneratedDrift reports whether model t has generated code whose
// recorded ModelHash no longer matches the model's current shape — i.e. the
// struct changed but `quark gen` was not re-run. It returns (drifted,
// hasGenerated): hasGenerated is false when t has no generated code at all
// (nothing to drift from). This is the optional stale-codegen check
// ADR-0014 requires F6-1 to make possible; the runtime never calls it
// automatically.
func CheckGeneratedDrift(t reflect.Type) (drifted bool, hasGenerated bool) {
	codegenMu.RLock()
	meta, ok := generatedMeta[modelKey(t)]
	codegenMu.RUnlock()
	if !ok {
		return false, false
	}
	return meta.ModelHash != ModelHash(modelKey(t)), true
}

// ModelField is one persisted field of a model, reduced to the attributes
// that define the model's shape. Both the runtime (from reflection) and the
// generator (from the AST) build a slice of these and feed it to
// HashModelFields, so the two derive identical hashes for an unchanged
// model. Exported so the generator can construct and hash fields with the
// exact same algorithm as the runtime.
type ModelField struct {
	// Name is the Go struct field name.
	Name string
	// Column is the column name from the db tag (sizing options stripped).
	Column string
	// GoType is the field's Go type, run through CanonicalType.
	GoType string
	// IsPK reports whether the field carries `pk:"true"`.
	IsPK bool
}

// HashModelFields returns a deterministic hash of a model's persisted shape
// from its fields. It is the single source of truth for the model hash: the
// runtime (ModelHash) and the generator both call it, so an unchanged model
// hashes identically on both sides. Field order does not matter.
func HashModelFields(fields []ModelField) string {
	lines := make([]string, 0, len(fields))
	for _, f := range fields {
		lines = append(lines, f.Name+"|"+f.Column+"|"+boolStr(f.IsPK)+"|"+CanonicalType(f.GoType))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

var canonTypeReplacer = regexp.MustCompile(`\b(byte|rune|any)\b`)

// CanonicalType normalizes a Go type string so the reflection renderer
// (reflect.Type.String) and the go/types renderer agree. They diverge on
// alias spellings: reflect prints byte as uint8, rune as int32, and the
// empty interface as "interface {}", while go/types may print "byte",
// "rune", and "any". Both sides run their type strings through this, so the
// hash is stable regardless of which spelling the source used.
func CanonicalType(s string) string {
	s = canonTypeReplacer.ReplaceAllStringFunc(s, func(m string) string {
		switch m {
		case "byte":
			return "uint8"
		case "rune":
			return "int32"
		case "any":
			return "interface {}"
		}
		return m
	})
	return s
}

// ModelHash returns a deterministic hash of model type t's persisted shape:
// for each exported field carrying a `db` tag, the field name, column name,
// primary-key flag, and Go type. It is the reflection-side caller of
// HashModelFields; the generator computes the same hash from the AST. The
// F6-1 conformance test asserts the two agree for a sample model, guarding
// against the two-tag-interpreters drift risk noted in ADR-0014.
func ModelHash(t reflect.Type) string {
	t = modelKey(t)
	if t == nil || t.Kind() != reflect.Struct {
		return ""
	}
	fields := make([]ModelField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		dbTag, ok := f.Tag.Lookup("db")
		if !ok {
			continue
		}
		// db:"-" / db:"" fields are not persisted (buildInsert/scanRow skip
		// them); exclude them from the hash so it matches the generator.
		col := columnFromDBTag(dbTag)
		if col == "" || col == "-" {
			continue
		}
		fields = append(fields, ModelField{
			Name:   f.Name,
			Column: col,
			GoType: f.Type.String(),
			IsPK:   strings.EqualFold(f.Tag.Get("pk"), "true"),
		})
	}
	return HashModelFields(fields)
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

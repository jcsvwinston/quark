package exercise

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// SECURITY ejerce el SQLGuard: identificadores, JSON-path y JOIN-ON hostiles
// deben rechazarse ANTES de tocar la BD. Verifica que la inyección se ataja
// (err != nil, query no ejecutada) y, donde Quark envuelve el sentinel con %w,
// que errors.Is lo alcanza.
var SECURITY = Exerciser{Name: "security", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, _ Conn) error {
	rec.Note(QF("For"), QM("Where"), QM("WhereJSON"), QM("Join"))

	// --- 1. Identificadores hostiles en Where → ValidateIdentifier rechaza ---
	hostile := []string{
		`id; DROP TABLE accounts;--`,
		`id) OR 1=1 --`,
		`name'`,
		`1=1`,
		`id'); DELETE FROM accounts;--`,
	}
	for _, h := range hostile {
		if _, err := quark.For[domain.Account](ctx, client).Where(h, "=", 1).List(); err == nil {
			return fmt.Errorf("identificador hostil %q NO fue rechazado", h)
		} else if !strings.Contains(strings.ToLower(err.Error()), "identifier") {
			return fmt.Errorf("identificador hostil %q: error inesperado: %v", h, err)
		}
	}

	// También por OrderBy (otra vía que valida el column en exec).
	if _, err := quark.For[domain.Account](ctx, client).OrderBy("name; DROP TABLE--", "ASC").Limit(1).List(); err == nil {
		return fmt.Errorf("OrderBy con columna hostil NO fue rechazado")
	}

	// --- 2. JSON-path hostil en WhereJSON → ErrInvalidJSONPath (envuelto) ---
	if _, err := quark.For[domain.Account](ctx, client).WhereJSON("settings", `theme'; DROP--`, "=", "x").List(); !errors.Is(err, quark.ErrInvalidJSONPath) {
		return fmt.Errorf("JSON-path hostil: esperaba ErrInvalidJSONPath, got %v", err)
	}

	// --- 3. JOIN ON hostil → ErrInvalidJoin (envuelto) ---
	if _, err := quark.For[domain.Task](ctx, client).Join("projects").On(`id; DROP TABLE projects`, "=", "x").List(); !errors.Is(err, quark.ErrInvalidJoin) {
		return fmt.Errorf("JOIN ON hostil: esperaba ErrInvalidJoin, got %v", err)
	}

	return nil
}}

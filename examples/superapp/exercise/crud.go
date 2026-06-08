package exercise

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// crudSeq da emails únicos entre motores y repeticiones (el atomic evita choques
// si el harness corriera exercisers en paralelo).
var crudSeq int64

// CRUD es el patrón canónico: Create → First → Count → Update → Delete(soft) →
// List, con un assert funcional por paso y marcando cada símbolo invocado.
var CRUD = Exerciser{Name: "crud", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	rec.Note(QF("For"))
	email := fmt.Sprintf("crud%d@superapp.test", atomic.AddInt64(&crudSeq, 1))

	// Create — el ID debe quedar asignado.
	a := &domain.Account{Email: email, Name: "crud", Role: "member", Active: true}
	if err := quark.For[domain.Account](rec.Mark(ctx, QM("Create")), client).Create(a); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if a.ID == 0 {
		return fmt.Errorf("create no asignó ID")
	}

	// First por email único — round-trip exacto.
	rec.Note(QM("Where"))
	got, err := quark.For[domain.Account](rec.Mark(ctx, QM("First")), client).Where("email", "=", email).First()
	if err != nil {
		return fmt.Errorf("first: %w", err)
	}
	if got.ID != a.ID || got.Email != email {
		return fmt.Errorf("first round-trip roto: got id=%d email=%q", got.ID, got.Email)
	}

	// Count — exactamente 1.
	n, err := quark.For[domain.Account](rec.Mark(ctx, QM("Count")), client).Where("email", "=", email).Count()
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("count=%d, esperaba 1", n)
	}

	// Update — cambia Name; persiste.
	got.Name = "crud-updated"
	rows, err := quark.For[domain.Account](rec.Mark(ctx, QM("Update")), client).Update(&got)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("update afectó %d filas, esperaba 1", rows)
	}

	// Re-lectura fresca (también renueva la versión para el delete con lock
	// optimista) y verificación de que el update persistió.
	fresh, err := quark.For[domain.Account](ctx, client).Where("id", "=", a.ID).First()
	if err != nil {
		return fmt.Errorf("reread: %w", err)
	}
	if fresh.Name != "crud-updated" {
		return fmt.Errorf("update no persistió: name=%q", fresh.Name)
	}

	// Delete (soft: Account tiene deleted_at) — la fila deja de contar.
	if _, err := quark.For[domain.Account](rec.Mark(ctx, QM("Delete")), client).Delete(&fresh); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	after, err := quark.For[domain.Account](ctx, client).Where("id", "=", a.ID).Count()
	if err != nil {
		return fmt.Errorf("count post-delete: %w", err)
	}
	if after != 0 {
		return fmt.Errorf("soft-delete no excluyó la fila: count=%d", after)
	}

	// List — termina ejerciendo el camino multi-fila.
	rec.Note(QM("Limit"))
	if _, err := quark.For[domain.Account](rec.Mark(ctx, QM("List")), client).Limit(10).List(); err != nil {
		return fmt.Errorf("list: %w", err)
	}
	return nil
}}

package exercise

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

var txSeq int64

// TX ejerce transacciones: un commit multi-entidad atómico (account + project) y
// un rollback (la closure devuelve error → nada persiste). Marca Client.Tx y
// ForTx.
var TX = Exerciser{Name: "tx", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	rec.Note(CM("Tx"), QF("ForTx"))
	n := atomic.AddInt64(&txSeq, 1)

	// --- Commit: account + project se confirman juntos ---
	email := fmt.Sprintf("tx%d@superapp.test", n)
	var accID int64
	err := client.Tx(rec.Mark(ctx, CM("Tx")), func(tx *quark.Tx) error {
		a := &domain.Account{Email: email, Name: "tx", Role: "member", Active: true}
		if err := quark.ForTx[domain.Account](ctx, tx).Create(a); err != nil {
			return err
		}
		accID = a.ID
		p := &domain.Project{OwnerID: a.ID, Name: "tx-proj", Status: "active"}
		return quark.ForTx[domain.Project](ctx, tx).Create(p)
	})
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if c, err := quark.For[domain.Account](ctx, client).Where("email", "=", email).Count(); err != nil || c != 1 {
		return fmt.Errorf("commit no persistió account (count=%d err=%v)", c, err)
	}
	if c, err := quark.For[domain.Project](ctx, client).Where("owner_id", "=", accID).Count(); err != nil || c != 1 {
		return fmt.Errorf("commit no persistió project (count=%d err=%v)", c, err)
	}

	// --- Rollback: la closure devuelve error → la fila no debe quedar ---
	rbEmail := fmt.Sprintf("txrb%d@superapp.test", n)
	sentinel := errors.New("rollback intencional")
	err = client.Tx(ctx, func(tx *quark.Tx) error {
		a := &domain.Account{Email: rbEmail, Name: "rb", Role: "member", Active: true}
		if cerr := quark.ForTx[domain.Account](ctx, tx).Create(a); cerr != nil {
			return cerr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		return fmt.Errorf("tx con error no propagó el sentinel: %v", err)
	}
	if c, err := quark.For[domain.Account](ctx, client).Where("email", "=", rbEmail).Count(); err != nil || c != 0 {
		return fmt.Errorf("rollback no revirtió (count=%d err=%v)", c, err)
	}
	return nil
}}

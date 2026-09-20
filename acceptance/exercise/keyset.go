// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package exercise

import (
	"context"
	"fmt"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/acceptance/domain"
	"github.com/jcsvwinston/quark/acceptance/recorder"
)

// KEYSET ejerce PaginateAfter (A8 S9) en los seis motores: cinco cuentas
// marcadas, páginas de dos con orden compuesto y un empate, y la exigencia de
// que cada fila salga exactamente una vez y en orden.
var KEYSET = Exerciser{Name: "keyset", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, _ Conn) error {
	accs := make([]*domain.Account, 5)
	for i := range accs {
		role := "member"
		if i%2 == 0 {
			role = "viewer"
		}
		accs[i] = &domain.Account{Email: fmt.Sprintf("kset-%02d@superapp.test", i), Name: fmt.Sprintf("kset%d", i), Role: role, Active: true}
	}
	if err := quark.For[domain.Account](ctx, client).CreateBatch(accs); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	defer func() {
		cctx := context.Background()
		rows, _ := quark.For[domain.Account](cctx, client).Where("email", "LIKE", "kset-%").WithTrashed().List()
		for i := range rows {
			_, _ = quark.For[domain.Account](cctx, client).HardDelete(&rows[i])
		}
	}()
	scoped := func(c context.Context) *quark.Query[domain.Account] {
		return quark.For[domain.Account](c, client).Where("email", "LIKE", "kset-%").OrderBy("role", "ASC").OrderBy("email", "DESC")
	}
	var seen []string
	token := ""
	for pages := 0; pages < 10; pages++ {
		page, err := scoped(rec.Mark(ctx, QM("PaginateAfter"))).PaginateAfter(2, token)
		if err != nil {
			return fmt.Errorf("PaginateAfter: %w", err)
		}
		for _, a := range page.Items {
			seen = append(seen, a.Email)
		}
		if !page.HasMore {
			break
		}
		token = page.Next
	}
	// role ASC (member < viewer), email DESC within each role.
	want := []string{"kset-03@superapp.test", "kset-01@superapp.test", "kset-04@superapp.test", "kset-02@superapp.test", "kset-00@superapp.test"}
	if len(seen) != len(want) {
		return fmt.Errorf("PaginateAfter: filas vistas %v, esperaba %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			return fmt.Errorf("PaginateAfter: orden %v, esperaba %v", seen, want)
		}
	}
	return nil
}}

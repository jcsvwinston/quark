// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

// controls assembles the catalogue from the five families.
//
// The families are separate files because they were measured separately and
// they fail separately: a change to the LIKE builder has no business making
// the migration catalogue conflict. What they share is the contract in
// enterprisebench_test.go — one probe per control, a recorded verdict, and a
// note that says what is missing whenever that verdict is not `present`.
func controls() []control {
	var all []control
	all = append(all, controlsMigraciones()...)
	all = append(all, controlsTipos()...)
	all = append(all, controlsRls()...)
	all = append(all, controlsOperacion()...)
	all = append(all, controlsQk25()...)
	return all
}

// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// controls assembles the catalogue from the three families.
//
// They are separate files because they answer to different deliverables of
// the arc — the published contract, the driver kit, the integrations — and a
// session that closes one has no business making another conflict. What they
// share is the contract in extbench_test.go: one probe per control, a
// recorded verdict, and a note that says what is missing whenever that
// verdict is not `present`.
func controls() []control {
	var all []control
	all = append(all, controlsContract()...)
	all = append(all, controlsDrivers()...)
	all = append(all, controlsIntegrations()...)
	return all
}

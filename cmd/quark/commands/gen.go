// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jcsvwinston/quark/cmd/quark/internal/codegen"
)

var codegenDryRun bool

var codegenCmd = &cobra.Command{
	Use:   "gen [packages]",
	Short: "Generate typed scanners/binders for models (opt-in codegen)",
	Long: `Generate parses the given package(s) and, for every struct with db: tags,
emits a quark_gen.go that registers a typed implementation with Quark's
runtime. Codegen is opt-in: without it the reflection path is used,
unchanged. The public API (quark.For[T]) is identical either way.

Drive it from a model package with:

	//go:generate quark gen ./...

What the generated code does today: List/First/Find use a typed row scanner
(no reflection), and Create uses a typed insert binder for single-integer-PK
models. Update, batch inserts, composite/non-integer keys, and the per-column
timezone feature still take the reflection path. Generated files carry a
versioned contract and fall back to reflection when it changes, so a stale
generated file is always safe.`,
	Args: cobra.ArbitraryArgs,
	// main.go prints the returned error and exits 1; silence cobra's own
	// error/usage dump so that single line is the only output (matches genCmd).
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		patterns := args
		if len(patterns) == 0 {
			patterns = []string{"./..."}
		}

		if codegenDryRun {
			pkgs, err := codegen.Load(patterns...)
			if err != nil {
				return err
			}
			for _, pm := range pkgs {
				src, err := codegen.Render(pm)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "// --- %s/%s ---\n%s\n", pm.PkgPath, codegen.GeneratedFileName, src)
			}
			return nil
		}

		written, err := codegen.Generate(patterns...)
		if err != nil {
			return err
		}
		for _, path := range written {
			fmt.Fprintln(cmd.OutOrStdout(), "wrote", path)
		}
		if len(written) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no models found")
		}
		return nil
	},
}

func init() {
	codegenCmd.Flags().BoolVar(&codegenDryRun, "dry-run", false, "print generated code to stdout instead of writing files")
	rootCmd.AddCommand(codegenCmd)
}

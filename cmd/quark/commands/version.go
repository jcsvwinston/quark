// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version can be stamped at build time:
//
//	go build -ldflags "-X github.com/jcsvwinston/quark/cmd/quark/commands.version=v1.2.0" ./cmd/quark
//
// When it is empty (the normal `go install .../cmd/quark@vX.Y.Z` path), the
// module version recorded in the binary's build info is used instead.
var version string

func cliVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "devel"
}

func init() {
	// Enables `quark --version` alongside the explicit subcommand.
	rootCmd.Version = cliVersion()
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the quark CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("quark %s %s/%s (%s)\n", cliVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	},
}

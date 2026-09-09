// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// libraryModule is the import path of the ORM this CLI drives. It is read out
// of the build info to report which release the binary carries.
const libraryModule = "github.com/jcsvwinston/quark"

// version is the CLI's own version, stamped at build time:
//
//	go build -ldflags "-X github.com/jcsvwinston/quark/cmd/quark/commands.version=v0.1.0" .
//
// When it is empty (the normal `go install .../cmd/quark@vX.Y.Z` path), the
// module version recorded in the binary's build info is used instead. Since
// ADR-0024 that is the version of the `cmd/quark` MODULE, whose tags are
// `cmd/quark/vX.Y.Z` and whose series is not the library's — so the release
// stamps the same series here, and a downloaded binary and an installed one
// report the same string.
var version string

// libraryVersion is the version of the library this binary was built against,
// stamped at build time for the same reason: the release builds inside the
// CLI module through a workspace, where the library is a local directory and
// build info records it as "(devel)". A `go install` build needs no stamp —
// the version is right there in the dependency list.
var libraryVersion string

func cliVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "devel"
}

// quarkLibraryVersion answers which release of the ORM is inside this binary.
// The CLI has its own version series (ADR-0024), so the two numbers are
// different and both are worth printing: the first says which CLI you are
// running, the second says which Quark it speaks for.
func quarkLibraryVersion() string {
	if libraryVersion != "" {
		return libraryVersion
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == libraryModule && dep.Version != "" && dep.Version != "(devel)" {
				return dep.Version
			}
		}
	}
	return "devel"
}

func init() {
	// Enables `quark --version` alongside the explicit subcommand.
	rootCmd.Version = cliVersion()
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:     "version",
	Example: `  quark version`,
	Short:   "Show the quark CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("quark %s %s/%s (%s)\n", cliVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
		fmt.Printf("built against %s %s\n", libraryModule, quarkLibraryVersion())
	},
}

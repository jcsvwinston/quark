package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "quark",
	Short: "Quark ORM CLI tool",
	Long: `Quark is a Go ORM for SQLite, PostgreSQL, MySQL, MariaDB, MSSQL and Oracle.
This CLI helps manage models, migrations, seeders, multi-tenancy, and code generation.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
//
// Execute returns the error instead of printing it: every subcommand sets
// SilenceErrors, so the CALLER owns reporting and the exit code. A main that
// ignores the return value turns every failure into a silent exit 0
// (QCD-CLI-2) — embedded runners should call Main instead unless they
// deliberately take over error handling.
func Execute() error {
	return rootCmd.Execute()
}

// Main runs the CLI with the standalone binary's error contract: execute the
// root command, print any error to stderr, exit 1. This is the entry point
// the embed recipe prescribes — it cannot be miswired the way a bare
// Execute() call can.
func Main() {
	if err := Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is .quark.yml)")
	rootCmd.PersistentFlags().Bool("debug", false, "enable debug mode")

	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName(".quark")
	}

	viper.SetEnvPrefix("QUARK")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		if viper.GetBool("debug") {
			fmt.Println("Using config file:", viper.ConfigFileUsed())
		}
	}
}

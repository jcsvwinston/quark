package commands

import (
	"fmt"
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
func Execute() error {
	return rootCmd.Execute()
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

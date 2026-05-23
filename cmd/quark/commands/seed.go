package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/fatih/color"
	"github.com/jcsvwinston/quark"
	clidb "github.com/jcsvwinston/quark/cmd/quark/internal/db"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	seedName string
	seedEnv  string
)

// SeederFunc is the signature for a registered seeder function.
type SeederFunc func(ctx context.Context, client *quark.Client) error

// seederRegistry holds seeders registered via RegisterSeeder.
var seederRegistry = map[string]SeederFunc{}

// RegisterSeeder registers a named seeder function.
// Call this from your main package before invoking commands.Execute().
func RegisterSeeder(name string, fn SeederFunc) {
	seederRegistry[name] = fn
}

func init() {
	seedCmd.AddCommand(seedCreateCmd)
	seedCmd.AddCommand(seedRunCmd)
	seedCmd.AddCommand(seedListCmd)

	seedCreateCmd.Flags().StringVar(&seedName, "name", "", "Name of the seeder")
	seedRunCmd.Flags().StringVar(&seedName, "name", "", "Name of the specific seeder to run (default: all)")
	seedRunCmd.Flags().StringVar(&seedEnv, "env", "development", "Environment")

	rootCmd.AddCommand(seedCmd)
}

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Manage database seeders",
}

var seedCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new seeder file",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runSeedCreate(args[0])
	},
}

var seedRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run seeders",
	Run: func(cmd *cobra.Command, args []string) {
		runSeedRun()
	},
}

var seedListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered seeders",
	Run: func(cmd *cobra.Command, args []string) {
		runSeedList()
	},
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) == 0 {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, "")
}

func runSeedCreate(name string) {
	filename := fmt.Sprintf("%s_seeder.go", name)
	dir := viper.GetString("paths.seeders")
	if dir == "" {
		dir = "./seeders"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		color.Red("Error creating seeders directory: %v", err)
		return
	}

	path := filepath.Join(dir, filename)

	data := struct {
		Name string
	}{
		Name: snakeToCamel(name),
	}

	tmpl, _ := template.New("seeder").Parse(seederTemplate)
	file, err := os.Create(path)
	if err != nil {
		color.Red("Error creating seeder file: %v", err)
		return
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		color.Red("Error executing template: %v", err)
		return
	}

	fmt.Printf("Created seeder: %s\n", path)
}

func runSeedRun() {
	if len(seederRegistry) == 0 {
		color.Yellow("No seeders registered.")
		color.Yellow("Register seeders before calling Execute():")
		fmt.Println()
		fmt.Println("  commands.RegisterSeeder(\"users\", seeders.SeedUsers)")
		fmt.Println("  commands.Execute()")
		return
	}

	client, err := clidb.GetQuarkClient()
	if err != nil {
		color.Red("Error connecting to database: %v", err)
		return
	}
	defer client.Close()

	ctx := context.Background()

	if seedName != "" {
		fn, ok := seederRegistry[seedName]
		if !ok {
			color.Red("Seeder %q not found. Use 'seed list' to see available seeders.", seedName)
			os.Exit(1)
		}
		color.Cyan("Running seeder: %s [env=%s]", seedName, seedEnv)
		if err := fn(ctx, client); err != nil {
			color.Red("Seeder %q failed: %v", seedName, err)
			os.Exit(1)
		}
		color.Green("Seeder %q completed successfully.", seedName)
		return
	}

	// Run all seeders in registration order
	color.Cyan("Running all seeders [env=%s]...", seedEnv)
	success, failed := 0, 0
	for name, fn := range seederRegistry {
		fmt.Printf("  Running %s...", name)
		if err := fn(ctx, client); err != nil {
			color.Red(" FAILED: %v", err)
			failed++
			continue
		}
		color.Green(" OK")
		success++
	}
	fmt.Printf("\nDone: %d succeeded, %d failed.\n", success, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func runSeedList() {
	if len(seederRegistry) == 0 {
		color.Yellow("No seeders registered.")
		return
	}
	color.Cyan("Registered seeders:")
	for name := range seederRegistry {
		fmt.Printf("  - %s\n", name)
	}
}

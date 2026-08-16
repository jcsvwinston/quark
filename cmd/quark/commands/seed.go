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
)

// SeederFunc is the signature for a registered seeder function.
type SeederFunc func(ctx context.Context, client *quark.Client) error

// seederRegistry holds seeders registered via RegisterSeeder; seederOrder
// remembers registration order, which is the order `seed run` promises. A map
// alone iterates randomly — every all-seeders run used a different order.
var (
	seederRegistry = map[string]SeederFunc{}
	seederOrder    []string
)

// RegisterSeeder registers a named seeder function.
// Call this from your main package before invoking commands.Main.
func RegisterSeeder(name string, fn SeederFunc) {
	if _, exists := seederRegistry[name]; !exists {
		seederOrder = append(seederOrder, name)
	}
	seederRegistry[name] = fn
}

func init() {
	seedCmd.AddCommand(seedCreateCmd)
	seedCmd.AddCommand(seedRunCmd)
	seedCmd.AddCommand(seedListCmd)

	seedCreateCmd.Flags().StringVar(&seedName, "name", "", "Name of the seeder")
	seedRunCmd.Flags().StringVar(&seedName, "name", "", "Name of the specific seeder to run (default: all)")

	rootCmd.AddCommand(seedCmd)
}

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Manage database seeders",
}

// Seeders run from scripts and CI, so a failing create/run must exit
// non-zero (RunE → main.go prints and exits 1).
var seedCreateCmd = &cobra.Command{
	Use:           "create <name>",
	Example:       `  quark seed create demo_users`,
	Short:         "Create a new seeder file",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSeedCreate(args[0])
	},
}

var seedRunCmd = &cobra.Command{
	Use: "run",
	Example: `  quark seed run
  quark seed run --name demo_users`,
	Short:         "Run seeders",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSeedRun()
	},
}

var seedListCmd = &cobra.Command{
	Use:     "list",
	Example: `  quark seed list`,
	Short:   "List registered seeders",
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

func runSeedCreate(name string) error {
	filename := fmt.Sprintf("%s_seeder.go", name)
	dir := viper.GetString("paths.seeders")
	if dir == "" {
		dir = "./seeders"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating seeders directory: %w", err)
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
		return fmt.Errorf("creating seeder file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("executing template: %w", err)
	}

	fmt.Printf("Created seeder: %s\n", path)
	return nil
}

func runSeedRun() error {
	// Same contract as migrate up/down (QK-P1-1): the standalone binary has
	// no way to see the project's seeders, so a "successful" run that seeded
	// nothing must exit non-zero, not 0.
	if len(seederRegistry) == 0 {
		return fmt.Errorf(`no seeders are registered in this binary — cannot run.

Seeders register via commands.RegisterSeeder from YOUR main before running
the CLI:

    commands.RegisterSeeder("users", seeders.SeedUsers)
    commands.Main()

commands.Main prints errors to stderr and exits non-zero on failure.

See the CLI guide ("Embedding the same operations in your own binary")`)
	}

	client, err := clidb.GetQuarkClient()
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer client.Close()

	ctx := context.Background()

	if seedName != "" {
		fn, ok := seederRegistry[seedName]
		if !ok {
			return fmt.Errorf("seeder %q not found; use 'seed list' to see available seeders", seedName)
		}
		color.Cyan("Running seeder: %s", seedName)
		if err := fn(ctx, client); err != nil {
			return fmt.Errorf("seeder %q failed: %w", seedName, err)
		}
		color.Green("Seeder %q completed successfully.", seedName)
		return nil
	}

	// Run all seeders in registration order
	color.Cyan("Running all seeders...")
	success, failed := 0, 0
	for _, name := range seederOrder {
		fn, ok := seederRegistry[name]
		if !ok {
			continue
		}
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
		return fmt.Errorf("%d of %d seeders failed", failed, success+failed)
	}
	return nil
}

func runSeedList() {
	if len(seederRegistry) == 0 {
		color.Yellow("No seeders registered.")
		return
	}
	color.Cyan("Registered seeders:")
	for _, name := range seederOrder {
		fmt.Printf("  - %s\n", name)
	}
}

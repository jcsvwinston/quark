package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/fatih/color"
	clidb "github.com/jcsvwinston/quark/cmd/quark/internal/db"
	"github.com/jcsvwinston/quark/seed"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	seedName string
)

// SeederFunc is the signature for a registered seeder function. It is an alias
// of seed.Func so the older `commands.RegisterSeeder(name, fn)` recipe keeps
// compiling; new code registers via a seeder file's init() with seed.Register.
type SeederFunc = seed.Func

// RegisterSeeder registers a named seeder function. It delegates to
// seed.Register so the CLI has a single registry regardless of whether the
// seeder self-registered (the generated files now do) or a main wired it by
// hand. Kept for the pre-1.8 embed recipe.
func RegisterSeeder(name string, fn SeederFunc) {
	seed.Register(name, fn)
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
		Name     string
		SeedName string
	}{
		Name:     snakeToCamel(name),
		SeedName: name,
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
	fmt.Printf("  It self-registers via init(); run it through your runner: go run ./cmd/<app> seed run\n")
	return nil
}

func runSeedRun() error {
	// Same contract as migrate up/down (QK-P1-1): the standalone binary has
	// no way to see the project's seeders, so a "successful" run that seeded
	// nothing must exit non-zero, not 0.
	if seed.Count() == 0 {
		return fmt.Errorf(`no seeders are registered in this binary — cannot run.

Seeders register themselves from an init(), like migrations. Files scaffolded
by 'quark seed create' already call seed.Register:

    func init() { seed.Register("users", SeedUsers) }

They reach the CLI when a runner blank-imports the seeders package (the runner
'quark init' writes already does this):

    import _ "github.com/you/yourapp/seeders" // side-effect: registers seeders

Build and run that runner, not the standalone 'quark' binary.

See the CLI guide ("Embedding the same operations in your own binary")`)
	}

	client, err := clidb.GetQuarkClient()
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer client.Close()

	ctx := context.Background()

	if seedName != "" {
		fn, ok := seed.Get(seedName)
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
	for _, name := range seed.Names() {
		fn, ok := seed.Get(name)
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
	if seed.Count() == 0 {
		color.Yellow("No seeders registered.")
		return
	}
	color.Cyan("Registered seeders:")
	for _, name := range seed.Names() {
		fmt.Printf("  - %s\n", name)
	}
}

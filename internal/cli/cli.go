// Package cli exposes the language-neutral Presto behavior through Cobra.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/brandnewteam/presto/internal/config"
	"github.com/brandnewteam/presto/internal/engine"
	"github.com/brandnewteam/presto/internal/lifecycle"
	"github.com/brandnewteam/presto/internal/remote"
	"github.com/spf13/cobra"
)

type options struct {
	global     string
	project    string
	json       bool
	noComposer bool
}

func New(version string, stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	var opts options
	root := &cobra.Command{Use: "presto", Short: "Build, sync and deploy PrestaShop modules", Version: version, SilenceUsage: true, SilenceErrors: true}
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().StringVar(&opts.global, "config", "", "global TOML config (or PRESTO_CONFIG)")
	root.PersistentFlags().StringVarP(&opts.project, "project", "C", "", "module project directory (default: discover above cwd)")
	root.PersistentFlags().BoolVar(&opts.json, "json", false, "machine-readable JSON output")
	root.PersistentFlags().BoolVar(&opts.noComposer, "no-composer", false, "use the current vendor directory without preparing dependencies")
	root.AddCommand(buildCommand(&opts), syncCommand(&opts, false), syncCommand(&opts, true), targetsCommand(&opts), doctorCommand(&opts), initCommand(&opts))
	root.AddCommand(&cobra.Command{Use: "receive", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		shop, _ := cmd.Flags().GetString("root")
		dry, _ := cmd.Flags().GetBool("dry-run")
		diff, _ := cmd.Flags().GetBool("diff")
		if shop == "" {
			return fmt.Errorf("receive requires --root")
		}
		encoded, _ := cmd.Flags().GetString("prestashop-json")
		var settings *config.PrestaShop
		if encoded != "" {
			settings = &config.PrestaShop{}
			decoder := json.NewDecoder(strings.NewReader(encoded))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(settings); err != nil {
				return fmt.Errorf("invalid lifecycle settings: %w", err)
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				return fmt.Errorf("trailing lifecycle settings")
			}
			if err := settings.Validate(); err != nil {
				return err
			}
		}
		result, err := engine.Receive(cmd.Context(), cmd.InOrStdin(), shop, dry, engine.SyncOptions{PrestaShop: settings, Logs: cmd.ErrOrStderr(), Diff: dry && diff})
		if err != nil {
			if result.Upgrade != nil && result.Error != "" {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}})
	receive, _, _ := root.Find([]string{"receive"})
	receive.Flags().String("root", "", "PrestaShop root")
	receive.Flags().String("prestashop-json", "", "private per-target lifecycle options")
	receive.Flags().Bool("dry-run", false, "compare without writing module files")
	receive.Flags().Bool("diff", false, "include diffs of updated files in dry-run results")
	agentInfo := &cobra.Command{Use: "agent-info", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		shop, _ := cmd.Flags().GetString("root")
		if shop != "" {
			if _, err := engine.ShopModule(shop, "prestocheck"); err != nil {
				return err
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(remote.Info{Protocol: engine.Protocol, Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, Capabilities: []string{lifecycle.Capability, engine.DiffCapability}})
	}}
	agentInfo.Flags().String("root", "", "check a PrestaShop root")
	root.AddCommand(agentInfo)
	return root
}

func load(opts *options, requireProject bool) (config.Config, string, error) {
	global := opts.global
	var err error
	if global == "" {
		global, err = config.GlobalPath()
	} else {
		global, err = config.ExpandPath(global, "")
	}
	if err != nil {
		return config.Config{}, "", err
	}
	start := opts.project
	if start == "" {
		start, err = os.Getwd()
		if err != nil {
			return config.Config{}, "", err
		}
	}
	project, projectErr := engine.FindProject(start)
	if projectErr != nil && (requireProject || opts.project != "") {
		return config.Config{}, "", projectErr
	}
	if project != "" {
		project, err = filepath.EvalSymlinks(project)
		if err != nil {
			return config.Config{}, "", err
		}
	}
	cfg, err := config.Load(global, project, opts.global != "" || os.Getenv("PRESTO_CONFIG") != "")
	return cfg, project, err
}

func buildCommand(opts *options) *cobra.Command {
	var output, version string
	var noVersion bool
	cmd := &cobra.Command{Use: "build", Short: "Create a distributable module ZIP", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, project, err := load(opts, true)
		if err != nil {
			return err
		}
		if version != "" {
			cfg.Module.Version = version
		}
		snapshot, cleanup, err := engine.Prepare(cmd.Context(), project, cfg, opts.noComposer, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		defer cleanup()
		prefix := "latest"
		if err := engine.AddConfigXML(snapshot, cfg.Module.ConfigXML); err != nil {
			return err
		}
		if !noVersion {
			prefix = "v" + snapshot.Version
		}
		destination := output
		if destination == "" {
			destination = filepath.Join(project, "dist", prefix+"-"+snapshot.Name+".zip")
		}
		destination, err = filepath.Abs(destination)
		if err != nil {
			return err
		}
		if !strings.EqualFold(filepath.Ext(destination), ".zip") {
			return fmt.Errorf("output must have a .zip extension")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(destination), ".presto-build-*.zip")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		writeErr := engine.WriteArchive(cmd.Context(), snapshot, file, false)
		if err := file.Chmod(0644); writeErr == nil {
			writeErr = err
		}
		if err := file.Close(); writeErr == nil {
			writeErr = err
		}
		if writeErr != nil {
			return writeErr
		}
		if err := os.Rename(file.Name(), destination); err != nil {
			return err
		}
		if opts.json {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"archive": destination, "module": snapshot.Name, "version": snapshot.Version, "files": len(snapshot.Files)})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Built %s (%d files)\n", destination, len(snapshot.Files))
		return nil
	}}
	cmd.Flags().StringVarP(&output, "output", "o", "", "ZIP output path")
	cmd.Flags().StringVar(&version, "module-version", "", "override the module version")
	cmd.Flags().BoolVarP(&noVersion, "no-version", "n", false, "write latest-<module>.zip (config.xml still requires a module version)")
	return cmd
}

func syncCommand(opts *options, deployment bool) *cobra.Command {
	name, description := "sync", "Synchronize a module into one or all configured local PrestaShop installations"
	usage, arguments := "sync [target]", cobra.MaximumNArgs(1)
	if deployment {
		name, description = "deploy", "Send one ZIP over SSH and synchronize it with the remote Go receiver"
		usage, arguments = "deploy <target>", cobra.ExactArgs(1)
	}
	var dryRun bool
	var colorMode string
	cmd := &cobra.Command{Use: usage, Short: description, Args: arguments, RunE: func(cmd *cobra.Command, args []string) error {
		color, err := colorEnabled(colorMode, cmd.OutOrStdout())
		if err != nil {
			return err
		}
		cfg, project, err := load(opts, true)
		if err != nil {
			return err
		}
		allTargets := !deployment && len(args) == 0
		targetNames := args
		if allTargets {
			for targetName, target := range cfg.Targets {
				if target.Type == "local" {
					targetNames = append(targetNames, targetName)
				}
			}
			sort.Strings(targetNames)
			if len(targetNames) == 0 {
				return fmt.Errorf("no local targets configured; run presto init --global")
			}
		}
		expected := "local"
		if deployment {
			expected = "ssh"
		}
		for _, targetName := range targetNames {
			target, ok := cfg.Targets[targetName]
			if !ok {
				return fmt.Errorf("unknown target %q; run presto targets", targetName)
			}
			if target.Type != expected {
				return fmt.Errorf("%s requires a %s target; %q is %s", name, expected, targetName, target.Type)
			}
			if !deployment {
				module := cfg.Module.Name
				if module == "" {
					module = filepath.Base(project)
				}
				destination, err := engine.ShopModule(target.Root, module)
				if err != nil {
					return fmt.Errorf("target %q: %w", targetName, err)
				}
				if engine.Overlaps(project, destination) {
					return fmt.Errorf("target %q: project and target overlap; use a separate module repository", targetName)
				}
			}
		}
		snapshot, cleanup, err := engine.Prepare(cmd.Context(), project, cfg, opts.noComposer, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		defer cleanup()
		if allTargets {
			return syncAllTargets(cmd, opts.json, color, cfg, targetNames, snapshot, dryRun)
		}
		target := cfg.Targets[targetNames[0]]
		var result engine.Result
		if deployment {
			if err := engine.AddConfigXML(snapshot, cfg.Module.ConfigXML); err != nil {
				return err
			}
			result, err = remote.Deploy(cmd.Context(), target, snapshot, dryRun, cmd.ErrOrStderr())
		} else {
			result, err = engine.Synchronize(cmd.Context(), snapshot, target.Root, dryRun, engine.SyncOptions{PrestaShop: target.PrestaShop, Logs: cmd.ErrOrStderr(), Diff: dryRun})
		}
		if err != nil {
			if opts.json && result.Upgrade != nil && result.Error != "" {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			} else if !opts.json {
				printFailure(cmd, targetNames[0], result, false)
			}
			return err
		}
		return printResult(cmd, opts.json, color, targetNames[0], result)
	}}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show content changes without modifying the destination")
	cmd.Flags().StringVar(&colorMode, "color", "auto", "color dry-run diffs: auto, always or never")
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		cfg, _, err := load(opts, false)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var names []string
		for targetName, target := range cfg.Targets {
			if (deployment && target.Type == "ssh") || (!deployment && target.Type == "local") {
				names = append(names, targetName)
			}
		}
		sort.Strings(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}

func printResult(cmd *cobra.Command, asJSON, color bool, target string, result engine.Result) error {
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	verb := "Synchronized"
	if result.DryRun {
		verb = "Would synchronize"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s → %s (%s)\n", verb, result.Module, target, result.Destination)
	for _, group := range []struct {
		prefix string
		paths  []string
	}{{"+", result.Created}, {"~", result.Updated}, {"-", result.Deleted}} {
		for _, path := range group.paths {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %s\n", group.prefix, path)
			if text, ok := result.Diffs[path]; ok && group.prefix == "~" {
				printDiff(cmd.OutOrStdout(), text, color)
			}
		}
	}
	printUpgrade(cmd.OutOrStdout(), result.Upgrade)
	fmt.Fprintf(cmd.OutOrStdout(), "%d created, %d updated, %d deleted, %d unchanged\n", len(result.Created), len(result.Updated), len(result.Deleted), result.Unchanged)
	return nil
}

// printFailure shows what a failed target attempted; main reports the error itself.
// Nothing is rolled back, so the planned changes and recovery state matter. Batches
// always name the failed target so their output keeps target order.
func printFailure(cmd *cobra.Command, target string, result engine.Result, always bool) {
	changes := len(result.Created) + len(result.Updated) + len(result.Deleted)
	if !always && result.Upgrade == nil && changes == 0 {
		return
	}
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "Failed to synchronize %s → %s (%s)\n", result.Module, target, result.Destination)
	if changes > 0 {
		fmt.Fprintf(w, "  planned %d created, %d updated, %d deleted; changes may be partly applied\n", len(result.Created), len(result.Updated), len(result.Deleted))
	}
	printUpgrade(w, result.Upgrade)
}

func printUpgrade(w io.Writer, upgrade *lifecycle.Result) {
	if upgrade == nil {
		return
	}
	fmt.Fprintf(w, "PrestaShop: %s (context %s, shops %v, version %s → %s)\n", upgrade.Status, upgrade.ShopContext, upgrade.ShopIDs, upgrade.FromVersion, upgrade.ToVersion)
	if upgrade.Status != "failed" {
		return
	}
	if upgrade.Disabled {
		fmt.Fprintf(w, "  module disabled in shops %v\n", upgrade.ShopIDs)
	} else if upgrade.EnabledShopIDs != nil {
		fmt.Fprintf(w, "  module still enabled in shops %v\n", upgrade.EnabledShopIDs)
	} else {
		fmt.Fprintln(w, "  activation state unknown")
	}
	if upgrade.RecoveryError != "" {
		fmt.Fprintf(w, "  recovery failed: %s\n", upgrade.RecoveryError)
	}
}

func targetsCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "targets", Short: "List named targets without connecting to them", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := load(opts, false)
		if err != nil {
			return err
		}
		if opts.json {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(cfg.Targets)
		}
		var names []string
		for name := range cfg.Targets {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No targets configured. Run presto init --global.")
		}
		for _, name := range names {
			target := cfg.Targets[name]
			location := target.Root
			if target.Type == "ssh" {
				location = target.Host + ":" + location
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", name, target.Type, location)
		}
		return nil
	}}
}

func doctorCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "doctor [target]", Short: "Check configuration and tools; optionally check a target", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, project, err := load(opts, false)
		if err != nil {
			return err
		}
		report := map[string]any{"configs": cfg.Loaded, "project": project, "platform": runtime.GOOS + "/" + runtime.GOARCH, "protocol": engine.Protocol}
		if image := cfg.Composer.Docker(); image != "" {
			if executable, err := exec.LookPath("docker"); err == nil {
				report["composer"] = fmt.Sprintf("%s in Docker image %s via %s", cfg.Composer.Command[0], image, executable)
			} else {
				report["composer"] = "docker not found (needed for composer.docker_image)"
			}
		} else if executable, err := exec.LookPath(cfg.Composer.Command[0]); err == nil {
			report["composer"] = executable
		} else {
			report["composer"] = "not found (needed only for Composer preparation)"
		}
		if executable, err := exec.LookPath("ssh"); err == nil {
			report["ssh"] = executable
		} else {
			report["ssh"] = "not found (needed for deploy)"
		}
		if len(args) > 0 {
			target, ok := cfg.Targets[args[0]]
			if !ok {
				return fmt.Errorf("unknown target %q", args[0])
			}
			if target.Type == "ssh" {
				info, err := remote.Inspect(cmd.Context(), target, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				report["receiver"] = info
			} else {
				destination, err := engine.ShopModule(target.Root, "prestocheck")
				if err != nil {
					return err
				}
				report["modules"] = filepath.Dir(destination)
				if target.PrestaShop != nil {
					report["prestashop"] = target.PrestaShop
				}
			}
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}}
}

func initCommand(opts *options) *cobra.Command {
	var global bool
	cmd := &cobra.Command{Use: "init", Short: "Create an example project or global TOML file without overwriting existing files", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		var destination, content string
		if global {
			destination = opts.global
			var err error
			if destination == "" {
				destination, err = config.GlobalPath()
			} else {
				destination, err = config.ExpandPath(destination, "")
			}
			if err != nil {
				return err
			}
			content = globalTemplate
		} else {
			base := opts.project
			if base == "" {
				var err error
				base, err = os.Getwd()
				if err != nil {
					return err
				}
			}
			destination = filepath.Join(base, "presto.toml")
			content = projectTemplate
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := io.WriteString(file, content)
		if err := file.Close(); writeErr == nil {
			writeErr = err
		}
		if writeErr != nil {
			return writeErr
		}
		absolute, _ := filepath.Abs(destination)
		fmt.Fprintln(cmd.OutOrStdout(), "Created", absolute)
		return nil
	}}
	cmd.Flags().BoolVar(&global, "global", false, "create the user config instead of project config")
	return cmd
}

const globalTemplate = `schema = 1

# Use the PrestaShop root; Presto appends modules/<module-name>.
# [targets.local]
# type = "local"
# root = "~/Sites/prestashop"

# Run Composer for every module in an image with the modules' PHP version.
# [composer]
# docker_image = "presto-composer:php7.1"

# Install the Linux Presto binary on the server before using deploy.
# [targets.production]
# type = "ssh"
# host = "my-ssh-alias"
# root = "/var/www/prestashop"
# agent = "~/.local/bin/presto"
`

const projectTemplate = `schema = 1

# Usually inferred from the project directory and a literal $this->version.
# [module]
# name = "mymodule"
# version = "1.0.0"

# Optional overrides for build/deploy config.xml metadata.
# [module.config_xml]
# display_name = "Example module"
# author = "Example author"

[files]
exclude = []
include = []
# Appends to the default uploads/ preservation rule.
preserve = []

[composer]
enabled = true
scripts = false
plugins = false
require_lock = false
# For legacy PHP: command = ["/path/to/php7.1", "/path/to/composer.phar"]
# Or run command inside an image with that PHP and Composer:
# docker_image = "presto-composer:php7.1"
# docker_args = ["--platform", "linux/amd64"]
`

// Package lifecycle invokes PrestaShop's own PHP APIs, never a Go reimplementation of migrations.
package lifecycle

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/brandnewteam/presto/internal/config"
)

const Capability = "prestashop16-upgrade-v1"

//go:embed prestashop16.php
var adapter []byte

// Result describes a lifecycle attempt, including recovery after an unsuccessful upgrade.
type Result struct {
	Status         string   `json:"status"`
	ShopContext    string   `json:"shop_context"`
	ShopID         int      `json:"shop_id,omitempty"`
	ShopGroupID    int      `json:"shop_group_id,omitempty"`
	ShopIDs        []int    `json:"shop_ids"`
	ModuleID       int      `json:"module_id,omitempty"`
	Installed      bool     `json:"installed"`
	EnabledShopIDs []int    `json:"enabled_shop_ids"`
	FromVersion    string   `json:"from_version,omitempty"`
	ToVersion      string   `json:"to_version,omitempty"`
	Applied        int      `json:"applied"`
	Disabled       bool     `json:"disabled"`
	Messages       []string `json:"messages"`
	Error          string   `json:"error,omitempty"`
	RecoveryError  string   `json:"recovery_error,omitempty"`
}

type request struct {
	Operation  string            `json:"operation"`
	Root       string            `json:"root"`
	Module     string            `json:"module"`
	Settings   config.PrestaShop `json:"settings"`
	NewVersion string            `json:"new_version"`
	Before     *Result           `json:"before,omitempty"`
	ResultPath string            `json:"result_path"`
}

type Runner struct {
	settings                         config.PrestaShop
	root, module, version, workspace string
	logs                             io.Writer
	before                           *Result
}

func Planned(settings config.PrestaShop) *Result {
	return &Result{Status: "planned", ShopContext: settings.ShopContext, ShopID: settings.ShopID, ShopGroupID: settings.ShopGroupID, ShopIDs: []int{}, Messages: []string{}}
}

func New(settings config.PrestaShop, root, module, version string, logs io.Writer) (*Runner, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if !settings.Upgrade {
		return nil, fmt.Errorf("lifecycle runner requires upgrade = true")
	}
	if logs == nil {
		logs = io.Discard
	}
	workspace, err := os.MkdirTemp("", "presto-lifecycle-")
	if err != nil {
		return nil, err
	}
	r := &Runner{settings: settings, root: root, module: module, version: version, workspace: workspace, logs: logs}
	if err := os.WriteFile(filepath.Join(workspace, "adapter.php"), adapter, 0600); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *Runner) Close() { _ = os.RemoveAll(r.workspace) }

// Preflight lints the module's own PHP and inspects installed state without loading the old module class.
func (r *Runner) Preflight(ctx context.Context, snapshotRoot string, files []string) (*Result, error) {
	planned := Planned(r.settings)
	planned.Status = "preflight_failed"
	if name, output, err := r.lint(ctx, snapshotRoot, files); err != nil {
		fmt.Fprint(r.logs, output)
		planned.Error = fmt.Sprintf("PHP lint failed for %s: %v", name, err)
		return planned, fmt.Errorf("PHP lint failed for %s: %w", name, err)
	}
	result, err := r.call(ctx, "inspect")
	if err != nil {
		if result != nil {
			planned = result
			planned.Status = "preflight_failed"
		}
		planned.Error = err.Error()
		return planned, fmt.Errorf("PrestaShop preflight: %w", err)
	}
	r.before = result
	result.Status = "pending"
	return result, nil
}

// Finalize always attempts independent recovery on failure, including a canceled upgrade process.
func (r *Runner) Finalize(ctx context.Context) (*Result, error) {
	result, err := r.call(ctx, "upgrade")
	if err == nil {
		return result, nil
	}
	if result == nil {
		copy := *r.before
		result = &copy
	}
	result.Status, result.Error = "failed", err.Error()
	result.ShopIDs = append([]int{}, r.before.ShopIDs...)
	result.ModuleID, result.FromVersion = r.before.ModuleID, r.before.FromVersion
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	result.EnabledShopIDs = nil
	recovery, recoveryErr := r.call(recoveryCtx, "disable")
	if recovery != nil {
		result.EnabledShopIDs = recovery.EnabledShopIDs
	}
	if recoveryErr != nil {
		result.RecoveryError = recoveryErr.Error()
		return result, fmt.Errorf("module upgrade failed: %w; could not confirm deactivation: %v", err, recoveryErr)
	}
	result.Disabled = recovery.Disabled
	if recovery.ToVersion != "" {
		result.ToVersion = recovery.ToVersion
	}
	if !result.Disabled {
		return result, fmt.Errorf("module upgrade failed: %w; deactivation could not be confirmed", err)
	}
	return result, fmt.Errorf("module upgrade failed: %w; module disabled in shop IDs %v", err, result.ShopIDs)
}

// lint checks PHP files with the target runtime, several processes at a time, and
// reports the first failure in path order. Composer resolved the vendor directory for
// the platform already; packages such as Symfony polyfills ship files for newer PHP
// versions that are only loaded there, so linting them would reject valid packages.
func (r *Runner) lint(ctx context.Context, snapshotRoot string, files []string) (string, string, error) {
	vendor := composerVendorDir(snapshotRoot)
	var names []string
	for _, name := range files {
		if strings.EqualFold(path.Ext(name), ".php") && name != vendor && !strings.HasPrefix(name, vendor+"/") {
			names = append(names, name)
		}
	}
	type failure struct {
		output string
		err    error
	}
	failures := make([]*failure, len(names))
	indexes := make(chan int)
	var wait sync.WaitGroup
	command := r.settings.Command()
	for worker := 0; worker < min(runtime.NumCPU(), 8, len(names)); worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := range indexes {
				args := append(append([]string{}, command[1:]...), "-l", filepath.Join(snapshotRoot, filepath.FromSlash(names[i])))
				// Successful lint output is noisy; only replay its diagnostic on failure.
				if output, err := exec.CommandContext(ctx, command[0], args...).CombinedOutput(); err != nil {
					failures[i] = &failure{string(output), err}
				}
			}
		}()
	}
	for i := range names {
		if ctx.Err() != nil {
			break
		}
		indexes <- i
	}
	close(indexes)
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	for i, failure := range failures {
		if failure != nil {
			return names[i], failure.output, failure.err
		}
	}
	return "", "", nil
}

// composerVendorDir reads vendor-dir from the prepared composer.json, as Composer does.
func composerVendorDir(root string) string {
	var settings struct {
		Config struct {
			VendorDir string `json:"vendor-dir"`
		} `json:"config"`
	}
	if data, err := os.ReadFile(filepath.Join(root, "composer.json")); err == nil && json.Unmarshal(data, &settings) == nil && settings.Config.VendorDir != "" {
		return strings.Trim(path.Clean(filepath.ToSlash(settings.Config.VendorDir)), "/")
	}
	return "vendor"
}

func (r *Runner) call(ctx context.Context, operation string) (*Result, error) {
	resultPath := filepath.Join(r.workspace, operation+"-result.json")
	req := request{operation, r.root, r.module, r.settings, r.version, r.before, resultPath}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	requestPath := filepath.Join(r.workspace, operation+"-request.json")
	if err := os.WriteFile(requestPath, data, 0600); err != nil {
		return nil, err
	}
	command := r.settings.Command()
	args := append(append([]string{}, command[1:]...), filepath.Join(r.workspace, "adapter.php"), requestPath)
	cmd := exec.CommandContext(ctx, command[0], args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = r.root, r.logs, r.logs
	runErr := cmd.Run()
	output, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		return nil, fmt.Errorf("%s process did not return a result (exit/timeout/fatal/bootstrap error): %v; %w", operation, runErr, readErr)
	}
	var result Result
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("invalid %s result: %w", operation, err)
	}
	if result.Error != "" {
		return &result, fmt.Errorf("%s", result.Error)
	}
	if runErr != nil {
		return &result, fmt.Errorf("%s process: %w", operation, runErr)
	}
	if result.Status != "inspected" && result.Status != "upgraded" && result.Status != "up_to_date" && result.Status != "not_installed" && result.Status != "disabled" {
		return &result, fmt.Errorf("incomplete %s result: %q", operation, result.Status)
	}
	return &result, nil
}

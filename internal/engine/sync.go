package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/brandnewteam/presto/internal/config"
	"github.com/brandnewteam/presto/internal/lifecycle"
	"io"
)

type Result struct {
	Module      string            `json:"module"`
	Destination string            `json:"destination"`
	DryRun      bool              `json:"dry_run"`
	Created     []string          `json:"created"`
	Updated     []string          `json:"updated"`
	Deleted     []string          `json:"deleted"`
	Unchanged   int               `json:"unchanged"`
	Upgrade     *lifecycle.Result `json:"upgrade,omitempty"`
	Error       string            `json:"error,omitempty"`
}

type SyncOptions struct {
	PrestaShop *config.PrestaShop
	Logs       io.Writer
}

// ShopModule refuses arbitrary destination directories; root must look like a shop.
func ShopModule(root, name string) (string, error) {
	if !ValidModule(name) {
		return "", fmt.Errorf("invalid module name %q", name)
	}
	expanded, err := config.ExpandPath(root, "")
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		return "", fmt.Errorf("shop root: %w", err)
	}
	marker, err := os.Stat(filepath.Join(resolved, "config", "config.inc.php"))
	if err != nil || !marker.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a PrestaShop root: missing config/config.inc.php", resolved)
	}
	modules := filepath.Join(resolved, "modules")
	info, err := os.Lstat(modules)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("shop modules must be an existing real directory: %s", modules)
	}
	destination := filepath.Join(modules, name)
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("module destination is not a real directory: %s", destination)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return destination, nil
}

func Overlaps(source, destination string) bool {
	for _, pair := range [][2]string{{source, destination}, {destination, source}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Synchronize computes content hashes even when file sizes and mtimes agree.
// Writes replace debugging edits; preserved paths are never visited or modified.
func Synchronize(ctx context.Context, snapshot *Snapshot, shopRoot string, dryRun bool, options ...SyncOptions) (result Result, resultErr error) {
	var opts SyncOptions
	if len(options) > 1 {
		return result, fmt.Errorf("only one sync options value is supported")
	}
	if len(options) == 1 {
		opts = options[0]
	}
	if opts.PrestaShop != nil {
		if err := opts.PrestaShop.Validate(); err != nil {
			return result, err
		}
	}
	enabled := opts.PrestaShop != nil && opts.PrestaShop.Upgrade
	destination, err := ShopModule(shopRoot, snapshot.Name)
	result = Result{Module: snapshot.Name, Destination: destination, DryRun: dryRun, Created: []string{}, Updated: []string{}, Deleted: []string{}}
	if err != nil {
		return result, err
	}
	if enabled {
		result.Upgrade = lifecycle.Planned(*opts.PrestaShop)
		defer func() {
			if resultErr != nil {
				result.Error = resultErr.Error()
				if result.Upgrade.Status == "pending" || result.Upgrade.Status == "planned" {
					result.Upgrade.Status = "not_run"
				}
			}
		}()
	}
	if Overlaps(snapshot.Root, destination) || (snapshot.Source != "" && Overlaps(snapshot.Source, destination)) {
		return result, fmt.Errorf("source and destination overlap")
	}
	if !dryRun {
		unlock, err := lockModule(destination)
		if err != nil {
			return result, err
		}
		defer unlock()
		// Validate again after acquiring the cooperative lock.
		if _, err := ShopModule(shopRoot, snapshot.Name); err != nil {
			return result, err
		}
	}
	wanted := make(map[string]File, len(snapshot.Files))
	directories := map[string]bool{}
	wantedCase := map[string]string{}
	for _, file := range snapshot.Files {
		wanted[file.Path] = file
		wantedCase[strings.ToLower(file.Path)] = file.Path
		for dir := path.Dir(file.Path); dir != "."; dir = path.Dir(dir) {
			directories[dir] = true
			wantedCase[strings.ToLower(dir)] = dir
		}
	}
	existing := map[string]fs.FileInfo{}
	if _, err := os.Lstat(destination); err == nil {
		err = filepath.WalkDir(destination, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rel, err := filepath.Rel(destination, current)
			if err != nil || rel == "." {
				return err
			}
			rel = filepath.ToSlash(rel)
			// Preserved symlinks, such as a shared uploads/ link, match like directories.
			if anyMatch(snapshot.Rules.Preserve, rel, entry.IsDir() || entry.Type()&os.ModeSymlink != 0) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if desired, ok := wantedCase[strings.ToLower(rel)]; ok && desired != rel {
				return fmt.Errorf("destination path %q differs in case from source %q; resolve the case rename manually", rel, desired)
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink at destination %s; exclude it with files.preserve or remove it manually", rel)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported destination file: %s", rel)
			}
			existing[rel] = info
			if info.IsDir() {
				if _, ok := wanted[rel]; ok {
					return fmt.Errorf("destination directory conflicts with source file %s; resolve it manually", rel)
				}
				return nil
			}
			if directories[rel] {
				return fmt.Errorf("destination file conflicts with source directory %s; resolve it manually", rel)
			}
			if _, ok := wanted[rel]; !ok {
				result.Deleted = append(result.Deleted, rel)
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	for _, file := range snapshot.Files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		info, exists := existing[file.Path]
		if !exists {
			result.Created = append(result.Created, file.Path)
			continue
		}
		if info.Size() != file.Size {
			result.Updated = append(result.Updated, file.Path)
			continue
		}
		hash, _, err := hashFile(filepath.Join(destination, filepath.FromSlash(file.Path)))
		if err != nil {
			return result, err
		}
		modeChanged := runtime.GOOS != "windows" && info.Mode().Perm() != fs.FileMode(file.Mode)
		if hash != file.SHA256 || modeChanged {
			result.Updated = append(result.Updated, file.Path)
		} else {
			result.Unchanged++
		}
	}
	sort.Strings(result.Deleted)
	sort.Strings(result.Created)
	sort.Strings(result.Updated)
	if dryRun {
		return result, nil
	}
	var runner *lifecycle.Runner
	if enabled {
		result.Upgrade.Status = "preflight_failed"
		runner, err = lifecycle.New(*opts.PrestaShop, filepath.Dir(filepath.Dir(destination)), snapshot.Name, snapshot.Version, opts.Logs)
		if err != nil {
			return result, err
		}
		defer runner.Close()
		names := make([]string, 0, len(snapshot.Files))
		for _, file := range snapshot.Files {
			names = append(names, file.Path)
		}
		result.Upgrade, err = runner.Preflight(ctx, snapshot.Root, names)
		if err != nil {
			return result, err
		}
	}
	// All validation and comparison finish before the first module file is changed.
	if err := os.MkdirAll(destination, 0755); err != nil {
		return result, err
	}
	for _, rel := range append(append([]string{}, result.Created...), result.Updated...) {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		file := wanted[rel]
		if err := copyFile(filepath.Join(snapshot.Root, filepath.FromSlash(rel)), filepath.Join(destination, filepath.FromSlash(rel)), fs.FileMode(file.Mode)); err != nil {
			return result, fmt.Errorf("write %s: %w", rel, err)
		}
	}
	for _, rel := range result.Deleted {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := os.Remove(filepath.Join(destination, filepath.FromSlash(rel))); err != nil {
			return result, fmt.Errorf("delete %s: %w", rel, err)
		}
	}
	var oldDirs []string
	for rel, info := range existing {
		if info.IsDir() && !directories[rel] {
			oldDirs = append(oldDirs, rel)
		}
	}
	sort.Slice(oldDirs, func(i, j int) bool { return len(oldDirs[i]) > len(oldDirs[j]) })
	for _, rel := range oldDirs {
		// Empty directories may be removed, but a directory containing runtime data stays.
		entries, err := os.ReadDir(filepath.Join(destination, filepath.FromSlash(rel)))
		if err != nil {
			return result, err
		}
		if len(entries) == 0 {
			if err := os.Remove(filepath.Join(destination, filepath.FromSlash(rel))); err != nil {
				return result, err
			}
		}
	}
	if runner != nil {
		result.Upgrade, err = runner.Finalize(ctx)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func lockModule(destination string) (func(), error) {
	base := filepath.Join(filepath.Dir(destination), ".presto-locks")
	if info, err := os.Lstat(base); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("invalid lock directory: %s", base)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, err
	}
	lock := filepath.Join(base, filepath.Base(destination))
	if err := os.Mkdir(lock, 0700); err != nil {
		return nil, fmt.Errorf("cannot lock module (another operation may be running; inspect %s): %w", lock, err)
	}
	return func() { _ = os.Remove(lock) }, nil
}

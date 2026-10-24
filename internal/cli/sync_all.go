package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brandnewteam/presto/internal/config"
	"github.com/brandnewteam/presto/internal/engine"
	"github.com/spf13/cobra"
)

type syncTargetResult struct {
	Target string `json:"target"`
	engine.Result
}

// Each target is independent after shared preparation. Finish the other targets
// after an apply failure, but propagate failure and stop on cancellation.
func syncAllTargets(cmd *cobra.Command, asJSON bool, cfg config.Config, names []string, snapshot *engine.Snapshot, dryRun bool) error {
	results := make([]syncTargetResult, 0, len(names))
	var failures []error
	for _, name := range names {
		if err := cmd.Context().Err(); err != nil {
			failures = append(failures, err)
			break
		}
		target := cfg.Targets[name]
		result, err := engine.Synchronize(cmd.Context(), snapshot, target.Root, dryRun, engine.SyncOptions{PrestaShop: target.PrestaShop, Logs: cmd.ErrOrStderr()})
		if err != nil {
			result.Error = err.Error()
			failures = append(failures, fmt.Errorf("target %q: %w", name, err))
		} else if !asJSON {
			if err := printResult(cmd, false, name, result); err != nil {
				failures = append(failures, err)
			}
		}
		results = append(results, syncTargetResult{Target: name, Result: result})
	}
	if asJSON {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(results); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

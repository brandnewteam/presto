// Package remote transports one validated ZIP to the Go receiver over system OpenSSH.
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/brandnewteam/presto/internal/config"
	"github.com/brandnewteam/presto/internal/engine"
	"github.com/brandnewteam/presto/internal/lifecycle"
	"slices"
)

type Info struct {
	Protocol     int      `json:"protocol"`
	Version      string   `json:"version"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Quote keeps remote arguments literal under a POSIX login shell.
func Quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func command(ctx context.Context, target config.Target, args ...string) (*exec.Cmd, error) {
	if target.Host == "" || target.Root == "" {
		return nil, fmt.Errorf("SSH target requires host and root")
	}
	agent := target.Agent
	if agent == "" {
		agent = "presto"
	}
	if strings.ContainsAny(agent+target.Root, "\x00\r\n") {
		return nil, fmt.Errorf("SSH paths must not contain control characters")
	}
	executable := Quote(agent)
	if strings.HasPrefix(agent, "~/") {
		executable = `"$HOME"/` + Quote(strings.TrimPrefix(agent, "~/"))
	}
	remote := "exec " + executable
	for _, arg := range args {
		remote += " " + Quote(arg)
	}
	sshArgs := []string{"-T", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}
	sshArgs = append(sshArgs, target.SSHArgs...)
	sshArgs = append(sshArgs, "--", target.Host, remote)
	return exec.CommandContext(ctx, "ssh", sshArgs...), nil
}

func Inspect(ctx context.Context, target config.Target, logs io.Writer) (Info, error) {
	var info Info
	cmd, err := command(ctx, target, "agent-info", "--root", target.Root)
	if err != nil {
		return info, err
	}
	cmd.Stderr = logs
	out, err := cmd.Output()
	if err != nil {
		return info, fmt.Errorf("remote receiver check failed: %w; install a matching presto binary and set targets.<name>.agent to its path", err)
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return info, fmt.Errorf("remote receiver returned invalid JSON (check shell startup output): %w", err)
	}
	if info.Protocol != engine.Protocol {
		return info, fmt.Errorf("remote protocol %d differs from local protocol %d", info.Protocol, engine.Protocol)
	}
	if target.PrestaShop != nil && target.PrestaShop.Upgrade && !slices.Contains(info.Capabilities, lifecycle.Capability) {
		return info, fmt.Errorf("remote receiver does not support PrestaShop 1.6 finalization; update the remote presto binary")
	}
	return info, nil
}

func Deploy(ctx context.Context, target config.Target, snapshot *engine.Snapshot, dryRun bool, logs io.Writer) (engine.Result, error) {
	var result engine.Result
	if _, err := Inspect(ctx, target, logs); err != nil {
		return result, err
	}
	archive, err := os.CreateTemp("", "presto-deploy-*.zip")
	if err != nil {
		return result, err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	if err := engine.WriteArchive(ctx, snapshot, archive, true); err != nil {
		return result, err
	}
	info, err := archive.Stat()
	if err != nil {
		return result, err
	}
	if info.Size() > engine.MaxArchiveBytes {
		return result, fmt.Errorf("deployment archive exceeds %d bytes", engine.MaxArchiveBytes)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	args := []string{"receive", "--root", target.Root}
	if target.PrestaShop != nil && target.PrestaShop.Upgrade {
		if err := target.PrestaShop.Validate(); err != nil {
			return result, err
		}
		data, err := json.Marshal(target.PrestaShop)
		if err != nil {
			return result, err
		}
		args = append(args, "--prestashop-json", string(data))
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	cmd, err := command(ctx, target, args...)
	if err != nil {
		return result, err
	}
	cmd.Stdin, cmd.Stderr = archive, logs
	out, err := cmd.Output()
	if err != nil {
		// A failed finalizer returns structured state as well as a nonzero exit.
		_ = json.Unmarshal(out, &result)
		return result, fmt.Errorf("remote deployment failed: %w (inspect destination before retrying if transfer was interrupted)", err)
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return result, fmt.Errorf("remote receiver returned invalid JSON: %w", err)
	}
	return result, nil
}

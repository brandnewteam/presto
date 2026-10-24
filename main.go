package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/brandnewteam/presto/internal/cli"
)

// version may be set by the release builder; otherwise module metadata is used.
var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer cancel()
	if err := cli.New(buildVersion(), os.Stdin, os.Stdout, os.Stderr).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "presto:", err)
		os.Exit(1)
	}
}

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"presto/internal/cli"
)

// version is replaced by the release builder.
var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer cancel()
	if err := cli.New(version, os.Stdin, os.Stdout, os.Stderr).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "presto:", err)
		os.Exit(1)
	}
}

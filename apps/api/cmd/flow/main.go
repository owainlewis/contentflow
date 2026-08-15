package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/owainlewis/contentflow/apps/api/internal/flowcli"
)

func main() {
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	exitCode := flowcli.Run(ctx, os.Args[1:], flowcli.Options{
		APIURL: os.Getenv("CONTENTFLOW_API_URL"),
		Token:  os.Getenv("CONTENTFLOW_API_TOKEN"),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	os.Exit(exitCode)
}

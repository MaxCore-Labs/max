package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"max/internal/adapter/mcpserver"
	"max/internal/platform"
)

// Version is set at build time via -ldflags "-X main.Version=..." and defaults to dev.
var Version = "0.1.0-dev"

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: max <command> [args] (try: max version)")
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		_, _ = fmt.Fprintf(stdout, "max %s\n", Version)
		return 0
	case "serve":
		return runServe(stdout, stderr)
	case "doctor":
		return runDoctor(stdout, stderr)
	case "migrate":
		return runMigrate(stdout, stderr)
	case "healthcheck":
		return runHealthcheck(stdout, stderr)
	case "engine":
		return runEngineCmd(args[1:], stdout, stderr)
	case "loadtest":
		return runLoadtest(args[1:], stdout, stderr)
	case "admin":
		return runAdmin(args[1:], os.Stdin, stdout, stderr)
	case "mcp":
		sock := os.Getenv("MAX_MCP_SOCK")
		if sock == "" {
			cfg, err := platform.Load(os.Getenv)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "load config: %v\n", err)
				return 1
			}
			sock = filepath.Join(cfg.DataDir, "run", "max.sock")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := mcpserver.RunStdio(ctx, sock); err != nil {
			_, _ = fmt.Fprintf(stderr, "max mcp: %v\n", err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

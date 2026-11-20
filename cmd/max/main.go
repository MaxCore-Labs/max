package main

import (
	"fmt"
	"io"
	"os"
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
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

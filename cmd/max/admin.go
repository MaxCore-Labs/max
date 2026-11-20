package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"max/internal/adapter/httpapi"
	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/platform"
)

func runAdmin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "set-password" {
		_, _ = fmt.Fprintln(stderr, "usage: max admin set-password [--password <pw>]")
		return 2
	}
	fs := flag.NewFlagSet("set-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pwFlag := fs.String("password", "", "new owner password (>= 12 chars)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	pw := strings.TrimSpace(*pwFlag)
	if pw == "" && stdin != nil {
		sc := bufio.NewScanner(stdin)
		if sc.Scan() {
			pw = strings.TrimSpace(sc.Text())
		}
	}
	if pw == "" {
		_, _ = fmt.Fprintln(stderr, "password is required (>= 12 chars)")
		return 1
	}

	cfg, err := platform.Load(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(cfg.DataDir, "max.sqlite3"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open sqlite: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	if err := httpapi.SetAdminPassword(ctx, st, app.SystemClock{}, app.UUIDv7Gen{}, pw); err != nil {
		_, _ = fmt.Fprintf(stderr, "set-password: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "owner password updated")
	return 0
}

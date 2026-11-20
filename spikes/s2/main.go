package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func findBunBinary() (string, error) {
	if p := os.Getenv("BUN_BIN"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("bun"); err == nil {
		return p, nil
	}
	if home := os.Getenv("HOME"); home != "" {
		cand := filepath.Join(home, ".bun", "bin", "bun")
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("bun not found in PATH")
}

func main() {
	target := flag.Int("target", 200000, "target memories for benchmark")
	agents := flag.Int("agents", 10000, "number of distinct agentIds")
	workers := flag.Int("workers", 32, "HTTP concurrent writers for agentmemory")
	maxWriteSec := flag.Int("max-write-sec", 90, "max seconds for agentmemory write phase before moving to search/restart")
	skipAM := flag.Bool("skip-am", false, "only run SQLite FTS5 benchmark")
	childCrashDB := flag.String("child-crash-db", "", "internal flag for SIGKILL persistence check")
	flag.Parse()

	if *childCrashDB != "" {
		if err := runChildCrashSeed(*childCrashDB); err != nil {
			fmt.Fprintf(os.Stderr, "child-crash-db: %v\n", err)
			os.Exit(1)
		}
		return
	}

	baseDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	if !*skipAM {
		bunExe, err := findBunBinary()
		if err != nil {
			fmt.Fprintf(os.Stderr, "find bun: %v\n", err)
			os.Exit(1)
		}
		if err := runAgentMemoryBenchmark(ctx, bunExe, *target, *agents, *workers, *maxWriteSec); err != nil {
			fmt.Fprintf(os.Stderr, "agentmemory benchmark error: %v\n", err)
			os.Exit(1)
		}
	}

	if err := runSQLiteFTS5Benchmark(ctx, baseDir, *target, *agents, 1000); err != nil {
		fmt.Fprintf(os.Stderr, "sqlite fts5 benchmark error: %v\n", err)
		os.Exit(1)
	}
}

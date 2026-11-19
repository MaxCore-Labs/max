package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func currentGoRSSMB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.Sys) / (1024 * 1024)
}

func findBunBinary() (string, error) {
	if p, err := exec.LookPath("bun"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("bun not found in PATH")
}

func main() {
	target := flag.Int("target", 200000, "target memories for agentmemory")
	agents := flag.Int("agents", 10000, "number of distinct agentIds")
	workers := flag.Int("workers", 32, "HTTP concurrent writers")
	maxWriteSec := flag.Int("max-write-sec", 180, "max seconds for agentmemory write phase before moving to search/restart")
	skipAM := flag.Bool("skip-am", false, "only run SQLite FTS5 benchmark")
	flag.Parse()

	baseDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if !*skipAM {
		bunExe, err := findBunBinary()
		if err != nil {
			fmt.Fprintf(os.Stderr, "find bun: %v\n", err)
			os.Exit(1)
		}
		if err := runAgentMemoryBenchmark(ctx, bunExe, baseDir, *target, *agents, *workers, *maxWriteSec); err != nil {
			fmt.Fprintf(os.Stderr, "agentmemory benchmark error: %v\n", err)
			os.Exit(1)
		}
	}

	if err := runSQLiteFTS5Benchmark(ctx, baseDir, 200000, *agents, 1000); err != nil {
		fmt.Fprintf(os.Stderr, "sqlite fts5 benchmark error: %v\n", err)
		os.Exit(1)
	}
}

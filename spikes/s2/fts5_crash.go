package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func runChildCrashSeed(dbPath string) error {
	ctx := context.Background()
	wdb, _, err := openFTSStore(ctx, dbPath)
	if err != nil {
		return err
	}
	nowMs := time.Now().UnixMilli()
	for i := 0; i < 200; i++ {
		tx, err := wdb.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := insertEpisodicTx(ctx, tx, buildSynthMemory(i, 50), nowMs+int64(i)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	// Signal parent that 200 single-row commits finished WITHOUT closing DB or checkpointing WAL.
	fmt.Println("READY_FOR_SIGKILL")
	time.Sleep(30 * time.Second)
	return nil
}

func verifySIGKILLPersistence(ctx context.Context, crashDB string) (int, int64, error) {
	selfExe, err := os.Executable()
	if err != nil {
		return 0, 0, err
	}
	cmd := exec.CommandContext(ctx, selfExe, "-child-crash-db", crashDB)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, 0, err
	}
	sc := bufio.NewScanner(stdout)
	ready := false
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "READY_FOR_SIGKILL" {
			ready = true
			break
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if !ready {
		return 0, 0, fmt.Errorf("child did not reach READY_FOR_SIGKILL")
	}

	t0 := time.Now()
	wdb, rdb, err := openFTSStore(ctx, crashDB)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		_ = rdb.Close()
		_ = wdb.Close()
	}()
	var count int
	if err := rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM episodic_memories`).Scan(&count); err != nil {
		return 0, 0, err
	}
	hits, err := searchEpisodic(ctx, rdb, "agent-00001", "", false, canaryToken, true, 5)
	if err != nil {
		return 0, 0, err
	}
	if len(hits) == 0 {
		return count, 0, fmt.Errorf("canary missing after SIGKILL")
	}
	return count, time.Since(t0).Milliseconds(), nil
}

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func main() {
	if os.Getenv("FAKECLI_CHILD_HANG") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		runACPMode(os.Args[1:])
		return
	}
	scriptPath := os.Getenv("FAKECLI_SCRIPT")
	if scriptPath == "" {
		os.Exit(0)
	}
	f, err := os.Open(scriptPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "open FAKECLI_SCRIPT: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = f.Close() }()

	br := bufio.NewReaderSize(f, 64*1024)
	out := bufio.NewWriterSize(os.Stdout, 64*1024)
	defer func() { _ = out.Flush() }()

	for {
		line, err := readFullLine(br)
		if len(line) > 0 {
			if errExec := execDirective(line, out); errExec != nil {
				_, _ = fmt.Fprintf(os.Stderr, "directive error: %v\n", errExec)
				os.Exit(2)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "read script: %v\n", err)
			os.Exit(2)
		}
	}
}

func readFullLine(br *bufio.Reader) (string, error) {
	var buf []byte
	for {
		frag, isPrefix, err := br.ReadLine()
		buf = append(buf, frag...)
		if err != nil {
			return string(buf), err
		}
		if !isPrefix {
			return string(buf), nil
		}
	}
}

func execDirective(line string, out *bufio.Writer) error {
	switch {
	case strings.HasPrefix(line, "#"):
		return nil
	case line == "hang":
		_ = out.Flush()
		for {
			time.Sleep(time.Hour)
		}
	case strings.HasPrefix(line, "sleep "):
		_ = out.Flush()
		ms, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "sleep ")))
		if err != nil {
			return err
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return nil
	case strings.HasPrefix(line, "exit "):
		_ = out.Flush()
		code, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "exit ")))
		if err != nil {
			return err
		}
		os.Exit(code)
		return nil
	case strings.HasPrefix(line, "partial "):
		_, _ = out.WriteString(strings.TrimPrefix(line, "partial "))
		return out.Flush()
	case strings.HasPrefix(line, "huge "):
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "huge ")))
		if err != nil {
			return err
		}
		chunk := bytes.Repeat([]byte{'x'}, min(n, 64*1024))
		rem := n
		for rem > 0 {
			w := min(rem, len(chunk))
			if _, err := out.Write(chunk[:w]); err != nil {
				return err
			}
			rem -= w
		}
		_ = out.WriteByte('\n')
		return out.Flush()
	case line == "spawn_child":
		self, err := os.Executable()
		if err != nil {
			return err
		}
		child := exec.Command(self)
		child.Env = append(os.Environ(), "FAKECLI_CHILD_HANG=1")
		if err := child.Start(); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "child_pid=%d\n", child.Process.Pid)
		return out.Flush()
	case line == "dump_env":
		b, err := json.Marshal(os.Environ())
		if err != nil {
			return err
		}
		_, _ = out.Write(b)
		_ = out.WriteByte('\n')
		return out.Flush()
	default:
		_, _ = out.WriteString(line)
		_ = out.WriteByte('\n')
		return out.Flush()
	}
}

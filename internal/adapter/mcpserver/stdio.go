package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
)

// RunStdio bridges os.Stdin / os.Stdout to the Max daemon's unix socket (used by `max mcp`).
func RunStdio(ctx context.Context, sock string) error {
	return RunBridgeIO(ctx, sock, os.Getenv("MAX_RUN_TOKEN"), os.Stdin, os.Stdout)
}

// RunBridgeIO dials sock, sends the MAX_RUN_TOKEN handshake header, and proxies MCP frames between in and out.
func RunBridgeIO(ctx context.Context, sock, token string, in io.Reader, out io.Writer) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return fmt.Errorf("dial max mcp socket %q: %w", sock, err)
	}
	defer func() { _ = conn.Close() }()

	hsBytes, err := json.Marshal(bridgeHandshake{RunToken: token})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(hsBytes, '\n')); err != nil {
		return fmt.Errorf("send mcp bridge handshake: %w", err)
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, in)
		if uconn, ok := conn.(*net.UnixConn); ok {
			_ = uconn.CloseWrite()
		}
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(out, conn)
		errCh <- err
	}()

	firstErr := <-errCh
	close(done)
	if ctx.Err() != nil {
		return nil
	}
	return firstErr
}

package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"max/internal/app/tools"
	"max/internal/domain"
)

type bridgeHandshake struct {
	RunToken string `json:"run_token"`
}

// Server hosts Max's MCP tool server over a local unix domain socket (<DataDir>/run/max.sock).
type Server struct {
	tools *tools.Service

	mu        sync.RWMutex
	byRun     map[domain.ID]activeRunEntry
	byToken   map[string]domain.ID
	byRootPID map[int]domain.ID
}

// NewServer constructs an MCP Server backed by tools.Service.
func NewServer(toolSvc *tools.Service) *Server {
	return &Server{
		tools:     toolSvc,
		byRun:     make(map[domain.ID]activeRunEntry),
		byToken:   make(map[string]domain.ID),
		byRootPID: make(map[int]domain.ID),
	}
}

// StartUnix binds sockPath (permissions 0600) and accepts bridged max mcp stdio connections until ctx ends.
func (s *Server) StartUnix(ctx context.Context, sockPath string) error {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return fmt.Errorf("mkdir mcp socket dir: %w", err)
	}
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen unix %q: %w", sockPath, err)
	}
	_ = os.Chmod(sockPath, 0o600)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
					return
				}
				continue
			}
			go s.handleConn(ctx, conn)
		}
	}()
	return nil
}

func (s *Server) handleConn(ctx context.Context, rawConn net.Conn) {
	defer func() { _ = rawConn.Close() }()

	var peerPID int
	if uconn, ok := rawConn.(*net.UnixConn); ok {
		peerPID = peerPIDFromConn(uconn)
	}

	_ = rawConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(rawConn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	_ = rawConn.SetReadDeadline(time.Time{})

	var hs bridgeHandshake
	if err := json.Unmarshal(bytes.TrimSpace(line), &hs); err != nil {
		return
	}

	caller, err := s.ResolveCaller(ctx, hs.RunToken, peerPID)
	if err != nil {
		return
	}

	mcpSrv := mcp.NewServer(&mcp.Implementation{
		Name:    "max",
		Version: "0.1.0",
	}, nil)
	registerCoreTools(mcpSrv, s.tools, caller)

	_ = mcpSrv.Run(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(br),
		Writer: nopWriteCloser{Writer: rawConn},
	})
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

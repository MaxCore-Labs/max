package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed all:web/build
var embeddedWeb embed.FS

type tickPayload struct {
	Seq          int64  `json:"seq"`
	ServerSentMs int64  `json:"serverSentMs"`
	Topic        string `json:"topic"`
}

func newHandler() (http.Handler, error) {
	distFS, err := fs.Sub(embeddedWeb, "web/build")
	if err != nil {
		return nil, fmt.Errorf("sub web/build: %w", err)
	}
	indexHTML, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded index.html: %w", err)
	}
	fileServer := http.FileServer(http.FS(distFS))

	mux := http.NewServeMux()
	var globalSeq atomic.Int64

	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		topic := r.URL.Query().Get("topics")
		if topic == "" {
			topic = "status"
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		sendTick := func() error {
			p := tickPayload{
				Seq:          globalSeq.Add(1),
				ServerSentMs: time.Now().UnixMilli(),
				Topic:        topic,
			}
			b, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "event: tick\ndata: %s\n\n", b); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}

		if err := sendTick(); err != nil {
			return
		}

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if err := sendTick(); err != nil {
					return
				}
			}
		}
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
			return
		}

		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean != "" {
			if info, err := fs.Stat(distFS, clean); err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexHTML)
	})

	return mux, nil
}

func runSelfTest(baseURL string) error {
	// 1. Direct reload of /activity must return 200 SPA index.html
	resp, err := http.Get(baseURL + "/activity")
	if err != nil {
		return fmt.Errorf("GET /activity: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "_app/immutable") {
		return fmt.Errorf("unexpected /activity response: status=%d len=%d", resp.StatusCode, len(body))
	}
	fmt.Printf("  [SelfTest] GET /activity -> HTTP %d (%d bytes SPA shell)\n", resp.StatusCode, len(body))

	// 2. Unknown /api/ route must NOT return HTML fallback
	apiResp, err := http.Get(baseURL + "/api/v1/nonexistent")
	if err != nil {
		return err
	}
	_ = apiResp.Body.Close()
	if apiResp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("expected 404 on unknown /api route, got %d", apiResp.StatusCode)
	}
	fmt.Printf("  [SelfTest] GET /api/v1/nonexistent -> HTTP %d (no HTML leak)\n", apiResp.StatusCode)

	// 3. Measure SSE delivery latency across 2 ticks
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/events?topics=status", nil)
	if err != nil {
		return err
	}
	sseResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect SSE: %w", err)
	}
	defer func() { _ = sseResp.Body.Close() }()

	sc := bufio.NewScanner(sseResp.Body)
	ticks := 0
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		recvMs := time.Now().UnixMilli()
		var p tickPayload
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p); err != nil {
			return err
		}
		lat := recvMs - p.ServerSentMs
		fmt.Printf("  [SelfTest] SSE tick #%d received in %d ms\n", p.Seq, lat)
		if lat >= 200 {
			return fmt.Errorf("SSE latency %d ms >= 200 ms", lat)
		}
		ticks++
		if ticks >= 2 {
			break
		}
	}
	if ticks < 2 {
		return fmt.Errorf("expected 2 SSE ticks, got %d", ticks)
	}
	return nil
}

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:18787", "address to listen on")
	selfTest := flag.Bool("self-test", false, "run automated SPA fallback and SSE latency check then exit")
	flag.Parse()

	handler, err := newHandler()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init handler: %v\n", err)
		os.Exit(1)
	}

	addr := *listenAddr
	if *selfTest {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen %s: %v\n", addr, err)
		os.Exit(1)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if *selfTest {
		go func() { _ = srv.Serve(ln) }()
		baseURL := "http://" + ln.Addr().String()
		if err := runSelfTest(baseURL); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
			os.Exit(1)
		}
		_ = srv.Shutdown(context.Background())
		return
	}

	fmt.Printf("Max S3 server listening on http://%s\n", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "serve error: %v\n", err)
		os.Exit(1)
	}
}

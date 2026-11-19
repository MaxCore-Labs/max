package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
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
	BootID       string `json:"bootId"`
	Seq          int64  `json:"seq"`
	ServerSentMs int64  `json:"serverSentMs"`
	Topic        string `json:"topic"`
}

func acceptsEncoding(header, target string) bool {
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, params, hasParams := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), target) {
			continue
		}
		if hasParams {
			for p := range strings.SplitSeq(params, ";") {
				k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
				if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
					q := strings.TrimSpace(v)
					if q == "0" || q == "0.0" || q == "0.00" || q == "0.000" {
						return false
					}
				}
			}
		}
		return true
	}
	return false
}

func contentTypeByPath(cleanPath string) string {
	ext := strings.ToLower(path.Ext(cleanPath))
	switch ext {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".html":
		return "text/html; charset=utf-8"
	default:
		if ct := mime.TypeByExtension(ext); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, distFS fs.FS, clean string, modTime time.Time) bool {
	w.Header().Set("Vary", "Accept-Encoding")
	ct := contentTypeByPath(clean)
	ae := r.Header.Get("Accept-Encoding")

	if acceptsEncoding(ae, "br") {
		if b, err := fs.ReadFile(distFS, clean+".br"); err == nil {
			w.Header().Set("Content-Encoding", "br")
			w.Header().Set("Content-Type", ct)
			http.ServeContent(w, r, path.Base(clean), modTime, bytes.NewReader(b))
			return true
		}
	}
	if acceptsEncoding(ae, "gzip") {
		if b, err := fs.ReadFile(distFS, clean+".gz"); err == nil {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", ct)
			http.ServeContent(w, r, path.Base(clean), modTime, bytes.NewReader(b))
			return true
		}
	}
	b, err := fs.ReadFile(distFS, clean)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, path.Base(clean), modTime, bytes.NewReader(b))
	return true
}

func newHandlerWithTimings(tickInterval, pingInterval time.Duration) (http.Handler, fs.FS, error) {
	distFS, err := fs.Sub(embeddedWeb, "web/build")
	if err != nil {
		return nil, nil, fmt.Errorf("sub web/build: %w", err)
	}
	spaHTML, err := fs.ReadFile(distFS, "200.html")
	if err != nil {
		spaHTML, err = fs.ReadFile(distFS, "index.html")
		if err != nil {
			return nil, nil, fmt.Errorf("read embedded SPA fallback (200.html/index.html): %w", err)
		}
	}

	bootID := fmt.Sprintf("b%x", time.Now().UnixNano())
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
				BootID:       bootID,
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

		tickTicker := time.NewTicker(tickInterval)
		defer tickTicker.Stop()
		pingTicker := time.NewTicker(pingInterval)
		defer pingTicker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-tickTicker.C:
				if err := sendTick(); err != nil {
					return
				}
			case <-pingTicker.C:
				if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
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
		if strings.HasSuffix(clean, ".br") || strings.HasSuffix(clean, ".gz") {
			http.NotFound(w, r)
			return
		}

		if clean != "" && clean != "200.html" && clean != "index.html" {
			if info, err := fs.Stat(distFS, clean); err == nil && !info.IsDir() {
				if strings.HasPrefix(clean, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "public, max-age=3600")
				}
				if serveStaticFile(w, r, distFS, clean, info.ModTime()) {
					return
				}
			}
			// Missing hashed chunks or static files with extensions must return 404, never SPA HTML.
			if strings.HasPrefix(clean, "_app/") || path.Ext(clean) != "" {
				http.NotFound(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(spaHTML)
	})

	return mux, distFS, nil
}

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:18787", "address to listen on")
	tickMs := flag.Int("tick-ms", 1000, "SSE tick interval in milliseconds")
	pingMs := flag.Int("ping-ms", 15000, "SSE keepalive ': ping' interval in milliseconds")
	selfTest := flag.Bool("self-test", false, "run automated SPA fallback, cache-header, and SSE check then exit")
	flag.Parse()

	pMs := *pingMs
	if *selfTest && pMs > 300 {
		pMs = 300
	}
	handler, distFS, err := newHandlerWithTimings(
		time.Duration(*tickMs)*time.Millisecond,
		time.Duration(pMs)*time.Millisecond,
	)
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
		if err := runSelfTest(baseURL, distFS); err != nil {
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

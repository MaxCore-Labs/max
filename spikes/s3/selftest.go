package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

func firstImmutableJS(distFS fs.FS) (string, error) {
	var found string
	err := fs.WalkDir(distFS, "_app/immutable", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(p, ".js") {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no .js chunk found under _app/immutable")
	}
	return found, nil
}

func runSelfTest(baseURL string, distFS fs.FS) error {
	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Direct reload of /activity must return 200 SPA shell with Cache-Control: no-cache
	resp, err := client.Get(baseURL + "/activity")
	if err != nil {
		return fmt.Errorf("GET /activity: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "_app/immutable") {
		return fmt.Errorf("unexpected /activity response: status=%d len=%d", resp.StatusCode, len(body))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		return fmt.Errorf("expected Cache-Control=no-cache on /activity, got %q", cc)
	}
	fmt.Printf("  [SelfTest] GET /activity -> HTTP %d (%d bytes SPA shell, Cache-Control=%s)\n", resp.StatusCode, len(body), resp.Header.Get("Cache-Control"))

	// 2. Unknown /api/ route must return 404 JSON
	apiResp, err := client.Get(baseURL + "/api/v1/nonexistent")
	if err != nil {
		return fmt.Errorf("GET /api/v1/nonexistent: %w", err)
	}
	_ = apiResp.Body.Close()
	if apiResp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("expected 404 on unknown /api route, got %d", apiResp.StatusCode)
	}
	fmt.Printf("  [SelfTest] GET /api/v1/nonexistent -> HTTP %d (no HTML leak)\n", apiResp.StatusCode)

	// 3. Missing chunk under /_app/immutable/ and missing static extension must return 404
	for _, missPath := range []string{"/_app/immutable/chunks/khongco.js", "/favicon-missing.ico"} {
		mResp, err := client.Get(baseURL + missPath)
		if err != nil {
			return fmt.Errorf("GET %s: %w", missPath, err)
		}
		_ = mResp.Body.Close()
		if mResp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("expected 404 on missing asset %s, got %d", missPath, mResp.StatusCode)
		}
		fmt.Printf("  [SelfTest] GET %s -> HTTP %d (strict asset 404)\n", missPath, mResp.StatusCode)
	}

	// 4. Existing immutable chunk must return Cache-Control: immutable and precompressed gzip
	jsChunk, err := firstImmutableJS(distFS)
	if err != nil {
		return err
	}
	chunkReq, err := http.NewRequest(http.MethodGet, baseURL+"/"+jsChunk, nil)
	if err != nil {
		return err
	}
	chunkReq.Header.Set("Accept-Encoding", "gzip")
	chunkResp, err := client.Do(chunkReq)
	if err != nil {
		return fmt.Errorf("GET /%s: %w", jsChunk, err)
	}
	_, _ = io.Copy(io.Discard, chunkResp.Body)
	_ = chunkResp.Body.Close()
	wantCC := "public, max-age=31536000, immutable"
	if chunkResp.StatusCode != http.StatusOK || chunkResp.Header.Get("Cache-Control") != wantCC {
		return fmt.Errorf("chunk headers mismatch: status=%d cc=%q", chunkResp.StatusCode, chunkResp.Header.Get("Cache-Control"))
	}
	if chunkResp.Header.Get("Content-Encoding") != "gzip" {
		return fmt.Errorf("expected precompressed Content-Encoding=gzip on %s, got %q", jsChunk, chunkResp.Header.Get("Content-Encoding"))
	}
	fmt.Printf("  [SelfTest] GET /%s -> HTTP 200 (Cache-Control=immutable, Content-Encoding=gzip)\n", jsChunk)

	// 5. Measure SSE delivery latency across 2 ticks + verify ": ping" keepalive comment
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/events?topics=status", nil)
	if err != nil {
		return err
	}
	sseClient := &http.Client{
		Transport: &http.Transport{DisableCompression: true},
	}
	sseResp, err := sseClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect SSE: %w", err)
	}
	defer func() { _ = sseResp.Body.Close() }()

	sc := bufio.NewScanner(sseResp.Body)
	ticks := 0
	pings := 0
	for sc.Scan() {
		line := sc.Text()
		if line == ": ping" {
			pings++
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		recvMs := time.Now().UnixMilli()
		var p tickPayload
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p); err != nil {
			return err
		}
		if p.BootID == "" {
			return fmt.Errorf("missing bootId in SSE tick payload")
		}
		lat := recvMs - p.ServerSentMs
		fmt.Printf("  [SelfTest] SSE tick #%d (boot=%s) received in %d ms\n", p.Seq, p.BootID, lat)
		if lat >= 200 {
			return fmt.Errorf("SSE latency %d ms >= 200 ms", lat)
		}
		ticks++
		if ticks >= 2 && pings >= 1 {
			break
		}
	}
	if ticks < 2 || pings < 1 {
		return fmt.Errorf("expected >=2 SSE ticks and >=1 ping, got ticks=%d pings=%d", ticks, pings)
	}
	fmt.Printf("  [SelfTest] SSE keepalive verified (%d ': ping' comment observed)\n", pings)
	return nil
}

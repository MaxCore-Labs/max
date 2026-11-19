package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *localCDPShim) handleFingerprintRoute(w http.ResponseWriter, r *http.Request) {
	// Expected: /fingerprint/{seed}/close OR /fingerprint/{seed}/devtools/...
	trimmed := strings.TrimPrefix(r.URL.Path, "/fingerprint/")
	seed, rest, ok := strings.Cut(trimmed, "/")
	if !ok || !safeSeedRe.MatchString(seed) {
		http.Error(w, "invalid fingerprint path", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && rest == "close" {
		s.closeSeed(seed, true)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"closed":true}`))
		return
	}
	if !strings.HasPrefix(rest, "devtools/") {
		http.NotFound(w, r)
		return
	}

	b, err := s.ensureSeed(r.Context(), seed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	upConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", b.cdpPort), 5*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	cliConn, cliBuf, err := hj.Hijack()
	if err != nil {
		_ = upConn.Close()
		return
	}

	outReq := r.Clone(context.Background())
	outReq.URL.Path = "/" + rest
	outReq.RequestURI = ""
	outReq.Host = fmt.Sprintf("127.0.0.1:%d", b.cdpPort)
	if err := outReq.Write(upConn); err != nil {
		_ = cliConn.Close()
		_ = upConn.Close()
		return
	}
	if n := cliBuf.Reader.Buffered(); n > 0 {
		buf := make([]byte, n)
		_, _ = cliBuf.Read(buf)
		_, _ = upConn.Write(buf)
	}

	go func() {
		_, _ = io.Copy(upConn, cliConn)
		_ = upConn.Close()
	}()
	go func() {
		_, _ = io.Copy(cliConn, upConn)
		_ = cliConn.Close()
	}()
}

func gracefulCDPBrowserClose(ctx context.Context, cdpPort int) {
	wsURL, err := fetchWSDebuggerURL(ctx, fmt.Sprintf("http://127.0.0.1:%d/json/version", cdpPort))
	if err != nil {
		return
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return
	}
	conn, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	var keyBytes [16]byte
	_, _ = rand.Read(keyBytes[:])
	wsKey := base64.StdEncoding.EncodeToString(keyBytes[:])
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", u.RequestURI(), u.Host, wsKey)
	if _, err := conn.Write([]byte(req)); err != nil {
		return
	}
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	payload := []byte(`{"id":1,"method":"Browser.close"}`)
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	frame := make([]byte, 0, 6+len(payload))
	frame = append(frame, 0x81, byte(0x80|len(payload)))
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, _ = conn.Write(frame)
	time.Sleep(250 * time.Millisecond)
}

func fetchWSDebuggerURL(ctx context.Context, versionURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := cdpHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var data struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.WS == "" {
		return "", fmt.Errorf("empty webSocketDebuggerUrl from %s", versionURL)
	}
	return data.WS, nil
}

package httpapi_test

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"max/internal/adapter/httpapi"
	"max/internal/app"
	"max/internal/platform"
	"max/internal/testutil/testdb"
)

func TestTopicFiltering(t *testing.T) {
	bus := platform.NewBus()
	ch1, cancel1 := bus.Subscribe([]string{"thread:1"}, 16)
	defer cancel1()

	bus.Publish(platform.Event{Topic: "thread:2", Type: "message", Data: "wrong"})
	bus.Publish(platform.Event{Topic: "thread:1", Type: "message", Data: "right"})

	select {
	case ev := <-ch1:
		if ev.Topic != "thread:1" || ev.Data != "right" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thread:1 event")
	}

	select {
	case extra := <-ch1:
		t.Fatalf("received leaked event from unsubscribed topic: %+v", extra)
	default:
	}
}

func newSSETestEnv(t *testing.T, pingCh <-chan time.Time) (*platform.Bus, *httpapi.Hub, http.Handler, *http.Cookie) {
	t.Helper()
	st := testdb.New(t)
	bus := platform.NewBus()
	hub := httpapi.NewHub(bus, app.SystemClock{})
	if pingCh != nil {
		hub.SetPingSource(func() (<-chan time.Time, func()) {
			return pingCh, func() {}
		})
	}

	h, err := httpapi.New(httpapi.Deps{
		Store: st,
		Bus:   bus,
		Hub:   hub,
		Config: platform.Config{
			Listen:        "127.0.0.1:8787",
			PublicURL:     "http://127.0.0.1:8787",
			AdminPassword: testAdminPassword,
		},
		Clock: app.SystemClock{},
		IDs:   app.UUIDv7Gen{},
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	loginRec := doLogin(h, "127.0.0.1:8787", testAdminPassword)
	return bus, hub, h, loginRec.Result().Cookies()[0]
}

func TestNoWildcardTopic(t *testing.T) {
	_, _, h, cookie := newSSETestEnv(t, nil)

	for _, badQuery := range []string{"?topics=*", "?topics=", "", "?topics=inbox,*"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/events"+badQuery, nil)
		req.Host = "127.0.0.1:8787"
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET /api/v1/events%s status = %d, want 400", badQuery, rec.Code)
		}
	}
}

type blockingFlushWriter struct {
	header  http.Header
	unblock chan struct{}
	started chan struct{}
	once    sync.Once
}

func (b *blockingFlushWriter) Header() http.Header { return b.header }
func (b *blockingFlushWriter) WriteHeader(int)     {}
func (b *blockingFlushWriter) Flush()              {}
func (b *blockingFlushWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.unblock
	return len(p), nil
}

func TestSlowClientDropped(t *testing.T) {
	bus, _, h, cookie := newSSETestEnv(t, nil)

	bw := &blockingFlushWriter{
		header:  make(http.Header),
		unblock: make(chan struct{}),
		started: make(chan struct{}),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events?topics=inbox", nil)
	req.Host = "127.0.0.1:8787"
	req.AddCookie(cookie)

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		h.ServeHTTP(bw, req)
	}()

	// Trigger 1st write so bw blocks inside Write, then blast 300 events (> 256 buffer).
	time.Sleep(30 * time.Millisecond)
	pubStart := time.Now()
	for i := range 300 {
		bus.Publish(platform.Event{Topic: "inbox", Type: "item", Data: i})
	}
	if elapsed := time.Since(pubStart); elapsed > 500*time.Millisecond {
		t.Fatalf("Publish blocked on slow client (%v)", elapsed)
	}

	close(bw.unblock)
	select {
	case <-handlerDone:
		// Slow client handler exited cleanly without request ctx cancellation!
	case <-time.After(2 * time.Second):
		t.Fatal("slow SSE client connection was not dropped after 256-event buffer overflow")
	}
}

func TestCountersThrottled(t *testing.T) {
	bus := platform.NewBus()
	hub := httpapi.NewHub(bus, app.SystemClock{})
	ch, cancel := bus.Subscribe([]string{"status"}, 64)
	defer cancel()

	for i := range 100 {
		hub.PublishStatus(map[string]int{"seq": i})
		time.Sleep(time.Millisecond)
	}

	count := 0
	drainDeadline := time.After(150 * time.Millisecond)
loop:
	for {
		select {
		case <-ch:
			count++
		case <-drainDeadline:
			break loop
		}
	}
	if count < 1 || count > 2 {
		t.Fatalf("status events emitted in 100ms burst = %d, want 1..2 (throttled at 1Hz)", count)
	}
}

func TestRunTextCap(t *testing.T) {
	bus := platform.NewBus()
	hub := httpapi.NewHub(bus, app.SystemClock{})

	head := strings.Repeat("H", 100*1024)
	tail := strings.Repeat("T", 200*1024)
	hub.AppendRunText("run-cap-1", head)
	hub.AppendRunText("run-cap-1", tail)

	snap, _, cancel := hub.RunText("run-cap-1")
	defer cancel()

	const wantCap = 256 * 1024
	if len(snap) != wantCap {
		t.Fatalf("len(snapshot) = %d, want %d (256 KB)", len(snap), wantCap)
	}
	if !strings.HasSuffix(snap, tail) {
		t.Fatal("snapshot did not retain the tail of the 300 KB stream")
	}
	if strings.HasPrefix(snap, strings.Repeat("H", 60*1024)) {
		t.Fatal("snapshot failed to drop head bytes beyond 256 KB")
	}
}

func TestPing(t *testing.T) {
	pingCh := make(chan time.Time, 4)
	_, _, h, cookie := newSSETestEnv(t, pingCh)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1:8787"
		h.ServeHTTP(w, r)
	}))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/events?topics=inbox", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Advance fake 20-second ping tick.
	pingCh <- time.Now()

	br := bufio.NewReader(resp.Body)
	lineCh := make(chan string, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					_ = bytes.ErrTooLarge
				}
				return
			}
			if strings.HasPrefix(line, ":") {
				lineCh <- line
				return
			}
		}
	}()

	select {
	case comment := <-lineCh:
		if !strings.HasPrefix(comment, ":") {
			t.Fatalf("expected SSE comment ping line, got %q", comment)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for 20s SSE comment ping")
	}
}

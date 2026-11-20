package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const sseSubscriberBuffer = 256

func isValidTopic(t string) bool {
	if t == "" || strings.Contains(t, "*") {
		return false
	}
	switch t {
	case "inbox", "status":
		return true
	}
	for _, prefix := range []string{"thread:", "task:", "agent:", "team:"} {
		if strings.HasPrefix(t, prefix) && len(t) > len(prefix) {
			return true
		}
	}
	return false
}

func parseTopicsParam(raw string) ([]string, bool) {
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if !isValidTopic(t) {
			return nil, false
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func (s *Server) handleStreamEvents(w http.ResponseWriter, r *http.Request) {
	topics, ok := parseTopicsParam(r.URL.Query().Get("topics"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid or wildcard topics parameter")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	evCh, cancelSub := s.deps.Bus.Subscribe(topics, sseSubscriberBuffer)
	defer cancelSub()

	pingCh, stopPing := s.deps.Hub.newPingChan()
	defer stopPing()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingCh:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-evCh:
			if !open {
				// Slow client buffer overflowed (256 events); drop connection so client reloads snapshot.
				return
			}
			payload, err := json.Marshal(map[string]any{
				"topic": ev.Topic,
				"type":  ev.Type,
				"data":  ev.Data,
			})
			if err != nil {
				continue
			}
			evName := ev.Type
			if evName == "" {
				evName = "message"
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evName, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleStreamRun(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.PathValue("runId"))
	if runID == "" {
		writeJSONError(w, http.StatusBadRequest, "missing runId")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	snap, deltas, cancel := s.deps.Hub.RunText(runID)
	defer cancel()

	if snap != "" {
		b, _ := json.Marshal(map[string]string{"snapshot": snap})
		_, _ = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b)
	}
	flusher.Flush()

	pingCh, stopPing := s.deps.Hub.newPingChan()
	defer stopPing()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingCh:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case delta, open := <-deltas:
			if !open {
				return
			}
			b, _ := json.Marshal(map[string]string{"delta": delta})
			if _, err := fmt.Fprintf(w, "event: delta\ndata: %s\n\n", b); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

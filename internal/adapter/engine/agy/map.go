package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/domain"
)

type cachedToolCall struct {
	Kind     string
	Name     string
	Title    string
	RawInput json.RawMessage
}

type envVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type mcpServerSpec struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args"`
	Env     []envVariable `json:"env"`
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type sessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Name          string          `json:"name"`
		Kind          string          `json:"kind"`
		Title         string          `json:"title"`
		Status        string          `json:"status"`
		RawInput      json.RawMessage `json:"rawInput"`
		Content       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"update"`
}

type permissionParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string          `json:"toolCallId"`
		Name       string          `json:"name"`
		Kind       string          `json:"kind"`
		Title      string          `json:"title"`
		RawInput   json.RawMessage `json:"rawInput"`
	} `json:"toolCall"`
	Options []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens      int64 `json:"inputTokens"`
		OutputTokens     int64 `json:"outputTokens"`
		CachedReadTokens int64 `json:"cachedReadTokens"`
		ThoughtTokens    int64 `json:"thoughtTokens"`
		TotalTokens      int64 `json:"totalTokens"`
	} `json:"usage"`
}

var rfc3339TimeRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

func defaultLimitPatterns() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bRESOURCE_EXHAUSTED\b`),
		regexp.MustCompile(`(?i)\bquota\s+exceeded\b`),
		regexp.MustCompile(`(?i)\bplan\s+limit\b`),
		regexp.MustCompile(`(?i)\brate[_ -]?limit(?:ed)?\b`),
		regexp.MustCompile(`(?i)\bHTTP(?:/\d(?:\.\d)?)?\s+429\b`),
		regexp.MustCompile(`(?i)\bstatus(?:\s+code)?[:=]?\s*429\b`),
		regexp.MustCompile(`(?i)\b429\s+too\s+many\s+requests\b`),
	}
}

func (d *Driver) matchesLimit(texts ...string) (bool, *time.Time) {
	joined := strings.Join(texts, "\n")
	if strings.TrimSpace(joined) == "" {
		return false, nil
	}
	hit := false
	for _, re := range d.limitPatterns {
		if re.MatchString(joined) {
			hit = true
			break
		}
	}
	if !hit {
		return false, nil
	}
	if m := rfc3339TimeRe.FindString(joined); m != "" {
		if ts, err := time.Parse(time.RFC3339, m); err == nil {
			return true, &ts
		}
	}
	return true, nil
}

func isKnownACPKind(kind string) bool {
	switch kind {
	case "execute", "read", "edit", "write", "other", "mcp", "fetch", "search":
		return true
	default:
		return false
	}
}

func (d *Driver) handleACPUpdate(
	raw json.RawMessage,
	cache map[string]cachedToolCall,
	nonText *[]string,
	out chan<- app.EngineEvent,
) {
	var p sessionUpdateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		if u.Content.Text != "" {
			out <- app.EngineEvent{Type: "text_delta", Text: u.Content.Text}
		}
	case "tool_call":
		prev := cache[u.ToolCallID]
		merged := mergeToolCache(prev, u.Kind, u.Name, u.Title, u.RawInput)
		cache[u.ToolCallID] = merged
		out <- app.EngineEvent{
			Type:         "tool_call",
			ToolUseID:    u.ToolCallID,
			ToolName:     merged.Name,
			InputPreview: string(merged.RawInput),
		}
	case "tool_call_update":
		prev := cache[u.ToolCallID]
		merged := mergeToolCache(prev, u.Kind, u.Name, u.Title, u.RawInput)
		cache[u.ToolCallID] = merged
		if u.Status == "completed" || u.Status == "failed" || u.Status == "error" {
			out <- app.EngineEvent{
				Type:      "tool_result",
				ToolUseID: u.ToolCallID,
				ToolName:  merged.Name,
				ToolOK:    u.Status == "completed",
			}
		}
	case "agent_thought_chunk":
	default:
		if len(*nonText) < 20 {
			*nonText = append(*nonText, string(raw))
		}
	}
}

func (d *Driver) handleACPPermission(
	ctx context.Context,
	spec app.RunSpec,
	perm app.PermissionFunc,
	reqID json.RawMessage,
	raw json.RawMessage,
	cache map[string]cachedToolCall,
	sendRaw func(any) error,
) {
	var p permissionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		_ = sendRaw(map[string]any{
			"jsonrpc": "2.0",
			"id":      reqID,
			"result":  map[string]any{"outcome": map[string]string{"outcome": "cancelled"}},
		})
		return
	}

	cached := cache[p.ToolCall.ToolCallID]
	merged := mergeToolCache(cached, p.ToolCall.Kind, p.ToolCall.Name, p.ToolCall.Title, p.ToolCall.RawInput)

	allow := false
	if isKnownACPKind(merged.Kind) && merged.Name != "" {
		if perm != nil {
			dec := perm(ctx, app.ToolAsk{
				RunToken: spec.RunToken,
				ToolName: merged.Name,
				Input:    merged.RawInput,
			})
			allow = dec.Verdict == domain.Allow
		}
	}

	targetKind := "reject_once"
	if allow {
		targetKind = "allow_once"
	}

	for _, opt := range p.Options {
		if opt.Kind == targetKind && opt.OptionID != "" {
			_ = sendRaw(map[string]any{
				"jsonrpc": "2.0",
				"id":      reqID,
				"result": map[string]any{
					"outcome": map[string]string{
						"outcome":  "selected",
						"optionId": opt.OptionID,
					},
				},
			})
			return
		}
	}

	d.log.Warn("acp permission options missing target kind; cancelling",
		slog.String("run_id", spec.RunID),
		slog.String("target_kind", targetKind),
	)
	_ = sendRaw(map[string]any{
		"jsonrpc": "2.0",
		"id":      reqID,
		"result": map[string]any{
			"outcome": map[string]string{"outcome": "cancelled"},
		},
	})
}

func (d *Driver) emitRPCFailure(
	runID string,
	rpcErr *rpcError,
	nonText []string,
	stderrText string,
	out chan<- app.EngineEvent,
) {
	errJSON, _ := json.Marshal(rpcErr)
	d.logLimitDiagnostics(runID, "rpc_error", string(errJSON), nonText, stderrText)
	if hit, resetAt := d.matchesLimit(rpcErr.Message, string(rpcErr.Data), stderrText); hit {
		out <- app.EngineEvent{Type: "limit", LimitResetAt: resetAt}
		return
	}
	out <- app.EngineEvent{
		Type:       "error",
		ErrKind:    fmt.Sprintf("rpc_%d", rpcErr.Code),
		ErrMessage: rpcErr.Message,
	}
}

func (d *Driver) logLimitDiagnostics(runID, reason, rawPayload string, updates []string, stderrText string) {
	d.log.Warn("agy non-normal stop diagnostic",
		slog.String("run_id", runID),
		slog.String("reason", reason),
		slog.String("rpc_payload", rawPayload),
		slog.Any("non_text_updates", updates),
		slog.String("stderr", stderrText),
	)
}

func mergeToolCache(prev cachedToolCall, kind, name, title string, rawIn json.RawMessage) cachedToolCall {
	out := prev
	if kind != "" {
		out.Kind = kind
	}
	if name != "" {
		out.Name = name
	}
	if title != "" {
		out.Title = title
	}
	if len(rawIn) > 0 && string(rawIn) != "null" {
		out.RawInput = rawIn
	}
	if len(out.RawInput) == 0 {
		out.RawInput = json.RawMessage(`{}`)
	}
	return out
}

func trimQuotes(s string) string {
	return strings.Trim(strings.TrimSpace(s), `"`)
}

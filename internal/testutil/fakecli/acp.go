package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"
)

// ACPScenario configures fakecli's simulated agy acp server for P1.18 driver tests.
type ACPScenario struct {
	RecordPath         string      `json:"record_path"`
	SessionID          string      `json:"session_id"`
	ReplayChunksOnLoad []string    `json:"replay_chunks_on_load"`
	SendToolCall       *ACPToolSim `json:"send_tool_call"`
	PermissionRequest  *ACPPermSim `json:"permission_request"`
	ResponseChunks     []string    `json:"response_chunks"`
	InputTokens        int64       `json:"input_tokens"`
	OutputTokens       int64       `json:"output_tokens"`
	StopReason         string      `json:"stop_reason"`
	RPCErrorCode       int         `json:"rpc_error_code"`
	RPCErrorMessage    string      `json:"rpc_error_message"`
	StderrText         string      `json:"stderr_text"`
}

type ACPToolSim struct {
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Title      string         `json:"title"`
	RawInput   map[string]any `json:"raw_input"`
}

type ACPPermSim struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"` // may be omitted to test toolCallId cache lookup
	Options    []ACPPermOption `json:"options"`
}

type ACPPermOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type recordedInteraction struct {
	Args         []string          `json:"args"`
	Env          []string          `json:"env"`
	Requests     []json.RawMessage `json:"requests"`
	PermResponse json.RawMessage   `json:"perm_response,omitempty"`
}

func runACPMode(args []string) {
	cfgPath := os.Getenv("FAKECLI_ACP_SCENARIO")
	var sc ACPScenario
	if cfgPath != "" {
		if b, err := os.ReadFile(cfgPath); err == nil {
			_ = json.Unmarshal(b, &sc)
		}
	}
	if sc.SessionID == "" {
		sc.SessionID = "sess-fake-acp-1"
	}
	if sc.StopReason == "" {
		sc.StopReason = "end_turn"
	}
	if sc.StderrText != "" {
		_, _ = fmt.Fprintln(os.Stderr, sc.StderrText)
	}

	rec := recordedInteraction{
		Args: args,
		Env:  os.Environ(),
	}
	saveRecord := func() {
		if sc.RecordPath == "" {
			return
		}
		if b, err := json.Marshal(rec); err == nil {
			_ = os.WriteFile(sc.RecordPath, b, 0o600)
		}
	}
	defer saveRecord()

	br := bufio.NewReaderSize(os.Stdin, 64*1024)
	out := bufio.NewWriterSize(os.Stdout, 64*1024)
	sendJSON := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = out.Write(b)
		_ = out.WriteByte('\n')
		_ = out.Flush()
	}

	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			trimmed := append([]byte(nil), bytesTrimSpace(line)...)
			rec.Requests = append(rec.Requests, json.RawMessage(trimmed))
			saveRecord()

			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				Result json.RawMessage `json:"result"`
			}
			_ = json.Unmarshal(trimmed, &msg)

			switch msg.Method {
			case "initialize":
				sendJSON(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"result": map[string]any{
						"protocolVersion": 1,
						"agentInfo":       map[string]any{"name": "Antigravity ACP Server", "version": "1.5.12"},
					},
				})
			case "session/new":
				sendJSON(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"result":  map[string]any{"sessionId": sc.SessionID},
				})
			case "session/load":
				for _, replay := range sc.ReplayChunksOnLoad {
					sendJSON(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"sessionId": sc.SessionID,
							"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": replay},
							},
						},
					})
				}
				sendJSON(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"result":  map[string]any{"sessionId": sc.SessionID},
				})
			case "session/set_mode":
				sendJSON(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"result":  map[string]any{},
				})
			case "session/prompt":
				handleFakePrompt(br, sendJSON, msg.ID, &sc, &rec, saveRecord)
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
	}
}

func handleFakePrompt(
	br *bufio.Reader,
	sendJSON func(any),
	reqID json.RawMessage,
	sc *ACPScenario,
	rec *recordedInteraction,
	saveRecord func(),
) {
	if sc.SendToolCall != nil {
		sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": sc.SessionID,
				"update": map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    sc.SendToolCall.ToolCallID,
					"name":          sc.SendToolCall.Name,
					"kind":          sc.SendToolCall.Kind,
					"title":         sc.SendToolCall.Title,
					"status":        "in_progress",
					"rawInput":      sc.SendToolCall.RawInput,
				},
			},
		})
	}

	if sc.PermissionRequest != nil {
		tc := map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    sc.PermissionRequest.ToolCallID,
			"status":        "pending",
		}
		if sc.PermissionRequest.Name != "" {
			tc["name"] = sc.PermissionRequest.Name
		}
		if sc.PermissionRequest.Kind != "" {
			tc["kind"] = sc.PermissionRequest.Kind
		}
		sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      "perm-1",
			"method":  "session/request_permission",
			"params": map[string]any{
				"sessionId": sc.SessionID,
				"toolCall":  tc,
				"options":   sc.PermissionRequest.Options,
			},
		})
		if respLine, err := br.ReadBytes('\n'); err == nil {
			rec.PermResponse = json.RawMessage(bytesTrimSpace(respLine))
			saveRecord()
		}
	}

	if sc.RPCErrorCode != 0 || sc.RPCErrorMessage != "" {
		code := sc.RPCErrorCode
		if code == 0 {
			code = -32603
		}
		sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      reqID,
			"error":   map[string]any{"code": code, "message": sc.RPCErrorMessage},
		})
		return
	}

	for _, ch := range sc.ResponseChunks {
		sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": sc.SessionID,
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": ch},
				},
			},
		})
	}

	minMS, _ := strconv.Atoi(os.Getenv("FAKECLI_ACP_SLEEP_MIN_MS"))
	maxMS, _ := strconv.Atoi(os.Getenv("FAKECLI_ACP_SLEEP_MAX_MS"))
	if maxMS > 0 && maxMS >= minMS {
		dur := minMS
		if maxMS > minMS {
			dur += rand.IntN(maxMS - minMS + 1)
		}
		if dur > 0 {
			time.Sleep(time.Duration(dur) * time.Millisecond)
		}
	}

	sendJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      reqID,
		"result": map[string]any{
			"stopReason": sc.StopReason,
			"usage": map[string]any{
				"inputTokens":  sc.InputTokens,
				"outputTokens": sc.OutputTokens,
			},
		},
	})
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

func (g *Gateway) executeWriteOnce(
	ctx context.Context,
	c Caller,
	info ConnInfo,
	toolDef ConnToolInfo,
	plainCfg []byte,
	secrets []string,
	input json.RawMessage,
	idemKey, targetVal string,
) Result {
	if g.isConversationTaskCancelled(ctx, c) {
		return Result{Text: "task đã bị hủy; bỏ qua thực thi", IsError: true}
	}

	fullName := info.Name + "." + toolDef.Name
	toolUseID := g.ids.New()
	nowMs := g.clock.Now().UnixMilli()
	convID := c.ConversationID
	inStr := string(canonicalJSON(input))
	var targetPtr *string
	if targetVal != "" {
		targetPtr = &targetVal
	}

	if err := g.store.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertToolCallStarted(ctx, app.ToolCallRecord{
			RunID:          c.RunID,
			ToolUseID:      toolUseID,
			ConversationID: &convID,
			ToolName:       fullName,
			Class:          string(domain.ToolClassE),
			TargetValue:    targetPtr,
			InputJSON:      &inStr,
			IdempotencyKey: &idemKey,
			StartedAt:      nowMs,
		})
	}); err != nil {
		if existing, found, _ := g.store.Read().FindToolCallByIdem(ctx, idemKey); found {
			if existing.Status == "done" && existing.Result != nil {
				return Result{Text: *existing.Result}
			}
			return Result{Text: "đang thực hiện (in progress)"}
		}
		return Result{Text: fmt.Sprintf("gateway start failed: %v", err), IsError: true}
	}

	cctx, cancel := context.WithTimeout(ctx, g.timeout)
	rawOut, invErr := g.invoker.Invoke(cctx, info.Kind, plainCfg, toolDef.Name, input)
	timedOut := errors.Is(cctx.Err(), context.DeadlineExceeded) || errors.Is(invErr, context.DeadlineExceeded)
	cancel()

	endedMs := g.clock.Now().UnixMilli()
	if invErr != nil {
		if timedOut || errors.Is(invErr, context.Canceled) {
			_ = g.store.WithTx(context.WithoutCancel(ctx), func(tx app.Tx) error {
				changed, err := tx.TransitionToolCallStatus(ctx, c.RunID, toolUseID, "started", "ambiguous", nil, &endedMs)
				if err != nil || !changed {
					return err
				}
				refKind := "tool_call"
				refID := fmt.Sprintf("%s:%s", c.RunID, toolUseID)
				agID := c.AgentID
				dedupe := "ambiguous:" + refID
				return tx.InsertInboxItem(ctx, app.InboxItemRecord{
					ID:             g.ids.New(),
					Kind:           "ambiguous",
					Title:          fmt.Sprintf("Lệnh %s tới %s có thể đã thực thi. Đã gửi chưa?", fullName, targetVal),
					Body:           "Lời gọi kết nối ngoài quá thời gian chờ; vui lòng kiểm tra đích và xác nhận đã gửi chưa.",
					RefKind:        &refKind,
					RefID:          &refID,
					ConversationID: &convID,
					AgentID:        &agID,
					Urgency:        "high",
					DedupeKey:      &dedupe,
					CreatedAt:      endedMs,
				})
			})
			return Result{Text: "lời gọi quá hạn (ambiguous): đang chờ chủ nhân xác nhận đã gửi chưa; không tự gửi lại", IsError: true}
		}

		errText := scrubSecrets(platform.Redact(invErr.Error()), secrets)
		_ = g.store.WithTx(context.WithoutCancel(ctx), func(tx app.Tx) error {
			_, err := tx.TransitionToolCallStatus(ctx, c.RunID, toolUseID, "started", "failed", &errText, &endedMs)
			return err
		})
		return Result{Text: errText, IsError: true}
	}

	cleanOut := scrubSecrets(platform.Redact(rawOut), secrets)
	var updated bool
	_ = g.store.WithTx(context.WithoutCancel(ctx), func(tx app.Tx) error {
		var err error
		updated, err = tx.TransitionToolCallStatus(ctx, c.RunID, toolUseID, "started", "done", &cleanOut, &endedMs)
		return err
	})
	if !updated {
		return Result{Text: "lời gọi đã chuyển trạng thái ambiguous trước đó", IsError: true}
	}
	g.recordRunEvent(ctx, c, fullName, cleanOut)
	return Result{Text: cleanOut}
}

// ResolveAmbiguous resolves an ambiguous outbound tool call per owner confirmation (sent=true marks done; sent=false re-sends once).
func (g *Gateway) ResolveAmbiguous(ctx context.Context, runID domain.ID, toolUseID string, sent bool) error {
	tc, ok, err := g.store.Read().GetToolCall(ctx, runID, toolUseID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tool_call %s/%s not found", runID, toolUseID)
	}
	if tc.Status != "ambiguous" {
		return nil
	}

	nowMs := g.clock.Now().UnixMilli()
	refID := fmt.Sprintf("%s:%s", runID, toolUseID)
	if sent {
		confirmed := "confirmed_sent_by_owner"
		return g.store.WithTx(ctx, func(tx app.Tx) error {
			if _, err := tx.TransitionToolCallStatus(ctx, runID, toolUseID, "ambiguous", "done", &confirmed, &nowMs); err != nil {
				return err
			}
			return tx.ResolveInboxByRef(ctx, "tool_call", refID, nowMs)
		})
	}

	connName, toolName, valid := strings.Cut(tc.ToolName, ".")
	if !valid {
		return fmt.Errorf("invalid tool_name %q", tc.ToolName)
	}
	info, _, plainCfg, secrets, errRes := g.lookupConnAndTool(ctx, connName, toolName)
	if errRes != nil {
		return errors.New(errRes.Text)
	}
	var rawIn json.RawMessage
	if tc.InputJSON != nil {
		rawIn = json.RawMessage(*tc.InputJSON)
	}
	cctx, cancel := context.WithTimeout(ctx, g.timeout)
	rawOut, invErr := g.invoker.Invoke(cctx, info.Kind, plainCfg, toolName, rawIn)
	cancel()
	if invErr != nil {
		return invErr
	}
	cleanOut := scrubSecrets(platform.Redact(rawOut), secrets)
	endedMs := g.clock.Now().UnixMilli()
	return g.store.WithTx(ctx, func(tx app.Tx) error {
		if _, err := tx.TransitionToolCallStatus(ctx, runID, toolUseID, "ambiguous", "done", &cleanOut, &endedMs); err != nil {
			return err
		}
		return tx.ResolveInboxByRef(ctx, "tool_call", refID, endedMs)
	})
}

func (g *Gateway) lookupConnAndTool(ctx context.Context, connName, toolName string) (ConnInfo, ConnToolInfo, []byte, []string, *Result) {
	rec, ok, err := g.store.Read().GetConnection(ctx, connName)
	if err != nil || !ok || !rec.Enabled {
		return ConnInfo{}, ConnToolInfo{}, nil, nil, &Result{Text: fmt.Sprintf("connection %q unavailable", connName), IsError: true}
	}
	var tools []ConnToolInfo
	_ = json.Unmarshal([]byte(rec.ToolsJSON), &tools)
	var found *ConnToolInfo
	for i := range tools {
		if tools[i].Name == toolName {
			found = &tools[i]
			break
		}
	}
	if found == nil {
		return ConnInfo{}, ConnToolInfo{}, nil, nil, &Result{Text: fmt.Sprintf("tool %q not registered on connection %q", toolName, connName), IsError: true}
	}

	var plain []byte
	var secrets []string
	if g.conns != nil {
		rv := reflect.ValueOf(g.conns)
		if m := rv.MethodByName("DecryptConfig"); m.IsValid() {
			outs := m.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(connName)})
			if len(outs) == 2 && outs[1].IsNil() {
				plain = outs[0].Bytes()
			}
		}
		if m := rv.MethodByName("SecretValues"); m.IsValid() && len(plain) > 0 {
			outs := m.Call([]reflect.Value{reflect.ValueOf(plain)})
			if len(outs) == 1 {
				secrets, _ = outs[0].Interface().([]string)
			}
		}
	}
	return ConnInfo{Name: rec.Name, Kind: rec.Kind, Enabled: rec.Enabled, Tools: tools}, *found, plain, secrets, nil
}

func (g *Gateway) askApproval(ctx context.Context, c Caller, fullName string, input json.RawMessage, reason string) (domain.ID, error) {
	if g.approvals == nil {
		return "", errors.New("approvals service unavailable")
	}
	rv := reflect.ValueOf(g.approvals)
	askMethod := rv.MethodByName("Ask")
	if !askMethod.IsValid() {
		return "", errors.New("approvals Ask method missing")
	}
	reqType := askMethod.Type().In(1)
	reqVal := reflect.New(reqType).Elem()
	reqVal.FieldByName("Caller").Set(reflect.ValueOf(c))
	reqVal.FieldByName("ToolName").SetString(fullName)
	reqVal.FieldByName("Input").Set(reflect.ValueOf(input))
	reqVal.FieldByName("Reason").SetString(reason)
	reqVal.FieldByName("ByMax").SetBool(true)
	outs := askMethod.Call([]reflect.Value{reflect.ValueOf(ctx), reqVal})
	if !outs[2].IsNil() {
		return "", outs[2].Interface().(error)
	}
	return outs[0].String(), nil
}

func (g *Gateway) resolveOrigin(ctx context.Context, c Caller) domain.RootOrigin {
	if c.TaskID != nil && *c.TaskID != "" {
		if tk, ok, _ := g.store.Read().GetTask(ctx, *c.TaskID); ok && tk.RootOrigin != "" {
			return tk.RootOrigin
		}
	}
	if c.Origin != "" {
		return c.Origin
	}
	return domain.OriginOwner
}

func (g *Gateway) isConversationTaskCancelled(ctx context.Context, c Caller) bool {
	var taskID string
	if c.TaskID != nil && *c.TaskID != "" {
		taskID = *c.TaskID
	} else if conv, ok, _ := g.store.Read().GetConversation(ctx, c.ConversationID); ok && conv.TaskID != nil {
		taskID = *conv.TaskID
	}
	if taskID == "" {
		return false
	}
	tk, ok, _ := g.store.Read().GetTask(ctx, taskID)
	return ok && tk.Status == string(domain.TaskCancelled)
}

func (g *Gateway) recordRunEvent(ctx context.Context, c Caller, toolName, cleanResult string) {
	data, _ := json.Marshal(map[string]string{"tool": toolName, "preview": truncateChars(cleanResult, 200)})
	nowMs := g.clock.Now().UnixMilli()
	_ = g.store.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertRunEvent(ctx, app.RunEventRecord{
			RunID:     c.RunID,
			AgentID:   c.AgentID,
			Type:      "gateway_call",
			DataJSON:  string(data),
			CreatedAt: nowMs,
		})
	})
}

func extractTargetField(input json.RawMessage, field string) string {
	if strings.TrimSpace(field) == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func scrubSecrets(s string, secrets []string) string {
	out := s
	for _, sec := range secrets {
		if sec != "" && strings.Contains(out, sec) {
			out = strings.ReplaceAll(out, sec, "[REDACTED]")
		}
	}
	return out
}

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/platform"
)

const defaultGatewayTimeout = 60 * time.Second

// ApprovalRequester abstracts approvals.Service.Ask to avoid a package import cycle between tools and approvals.
type ApprovalRequester interface {
	Ask(ctx context.Context, r GatewayApprovalRequest) (approvalID domain.ID, alreadyPending bool, err error)
}

// GatewayApprovalRequest mirrors approvals.Request for outbound class E gateway approvals.
type GatewayApprovalRequest struct {
	Caller   Caller
	ToolName string
	Input    json.RawMessage
	Reason   string
	ByMax    bool
}

// GrantLookup abstracts grants.Service for Gateway evaluation and daily count tracking.
type GrantLookup interface {
	Active(ctx context.Context, agentID domain.ID, now time.Time) ([]domain.Grant, error)
	CountUse(ctx context.Context, tx app.Tx, id domain.ID, now time.Time) (bool, error)
}

// ConnProvider abstracts connections.Service for Gateway tool resolution and secret scrubbing.
type ConnProvider interface {
	Get(ctx context.Context, name string) (ConnInfo, bool, error)
	DecryptConfig(ctx context.Context, name string) ([]byte, error)
	SecretValues(plain []byte) []string
}

// ConnInfo describes a connection's enabled flag, transport kind, and tool definitions.
type ConnInfo struct {
	Name    string
	Kind    string
	Enabled bool
	Tools   []ConnToolInfo
}

// ConnToolInfo describes one external tool's read/write class and optional target field.
type ConnToolInfo struct {
	Name        string `json:"name"`
	Write       bool   `json:"write"`
	TargetField string `json:"target_field,omitempty"`
}

type approvedEnvelope struct {
	Input         json.RawMessage `json:"input"`
	RepeatToken   string          `json:"repeat_token,omitempty"`
	ConfirmRepeat bool            `json:"confirm_repeat,omitempty"`
}

// GatewayDeps configures the single outbound external-action Gateway (Spec 8.5.3).
type GatewayDeps struct {
	Store       app.Store
	Connections any // *connections.Service or ConnProvider
	Grants      GrantLookup
	Approvals   any // *approvals.Service or ApprovalRequester
	Actors      *actor.Service
	Invoker     app.OutboundInvoker
	Timeout     time.Duration
	Clock       app.Clock
	IDs         app.IDGen
}

// Gateway enforces single-path outbound execution with idempotency keys, target repeat guard, grants, and ambiguous handling.
type Gateway struct {
	store     app.Store
	conns     any
	grants    GrantLookup
	approvals any
	actors    *actor.Service
	invoker   app.OutboundInvoker
	timeout   time.Duration
	clock     app.Clock
	ids       app.IDGen
}

// NewGateway constructs a Gateway.
func NewGateway(d GatewayDeps) *Gateway {
	if d.Timeout <= 0 {
		d.Timeout = defaultGatewayTimeout
	}
	if d.Clock == nil {
		d.Clock = app.SystemClock{}
	}
	if d.IDs == nil {
		d.IDs = app.UUIDv7Gen{}
	}
	return &Gateway{
		store:     d.Store,
		conns:     d.Connections,
		grants:    d.Grants,
		approvals: d.Approvals,
		actors:    d.Actors,
		invoker:   d.Invoker,
		timeout:   d.Timeout,
		clock:     d.Clock,
		ids:       d.IDs,
	}
}

// IdempotencyKey computes sha256(conversationID, tool, canonicalInput, repeatToken) (Spec 8.5.3).
func IdempotencyKey(conversationID domain.ID, tool string, input json.RawMessage, repeatToken string) string {
	canon := canonicalJSON(input)
	h := sha256.New()
	_, _ = h.Write([]byte(conversationID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(tool))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(canon)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(repeatToken))
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalJSON(raw json.RawMessage) []byte {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return []byte("{}")
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		if b, mErr := json.Marshal(v); mErr == nil {
			return b
		}
	}
	return []byte(trimmed)
}

// Call executes or parks an external connection tool call through Max's single outbound gateway.
func (g *Gateway) Call(
	ctx context.Context,
	c Caller,
	connName, toolName string,
	input json.RawMessage,
	repeatToken string,
	confirmRepeat bool,
) Result {
	info, toolDef, plainCfg, secrets, errRes := g.lookupConnAndTool(ctx, connName, toolName)
	if errRes != nil {
		return *errRes
	}
	fullName := connName + "." + toolName

	if !toolDef.Write {
		dec := domain.Decide(c.Policy, domain.ToolCall{
			Name: toolName, Class: domain.ToolClassR, Connection: connName, Input: input,
		}, c.Origin, nil, g.clock.Now())
		if dec.Verdict != domain.Allow {
			return Result{Text: fmt.Sprintf("tool %s denied: %s", fullName, dec.Reason), IsError: true}
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, err := g.invoker.Invoke(rctx, info.Kind, plainCfg, toolName, input)
		cancel()
		if err != nil {
			return Result{Text: scrubSecrets(platform.Redact(err.Error()), secrets), IsError: true}
		}
		clean := scrubSecrets(platform.Redact(out), secrets)
		g.recordRunEvent(ctx, c, fullName, clean)
		return Result{Text: clean}
	}

	if c.Policy.Autonomy == domain.AutonomySuggest {
		return Result{Text: "suggest-mode agent cannot execute external write tools", IsError: true}
	}

	idemKey := IdempotencyKey(c.ConversationID, fullName, input, repeatToken)
	if existing, found, _ := g.store.Read().FindToolCallByIdem(ctx, idemKey); found {
		switch existing.Status {
		case "done":
			if existing.Result != nil {
				return Result{Text: *existing.Result}
			}
			return Result{Text: "ok"}
		case "started":
			return Result{Text: "đang thực hiện (in progress)"}
		case "ambiguous":
			return Result{Text: "trạng thái ambiguous: đang chờ chủ nhân xác nhận đã gửi chưa", IsError: true}
		}
	}

	targetVal := extractTargetField(input, toolDef.TargetField)
	if targetVal != "" && !confirmRepeat {
		if prev, found, _ := g.store.Read().FindDoneToolCallByTarget(ctx, c.ConversationID, fullName, targetVal); found {
			return Result{
				Text:    fmt.Sprintf("Cảnh báo: đã gửi tới đích %q lúc %d; truyền confirm_repeat=true nếu cố ý gửi lại.", targetVal, prev.StartedAt),
				IsError: true,
			}
		}
	}

	now := g.clock.Now()
	var activeGrants []domain.Grant
	if g.grants != nil {
		activeGrants, _ = g.grants.Active(ctx, c.AgentID, now)
	}
	origin := g.resolveOrigin(ctx, c)
	dec := domain.Decide(c.Policy, domain.ToolCall{
		Name: toolName, Class: domain.ToolClassE, Connection: connName, Input: input,
	}, origin, activeGrants, now)

	if dec.Verdict == domain.Allow && dec.GrantID != "" && g.grants != nil {
		var okDay bool
		_ = g.store.WithTx(ctx, func(tx app.Tx) error {
			var cerr error
			okDay, cerr = g.grants.CountUse(ctx, tx, dec.GrantID, now)
			return cerr
		})
		if !okDay {
			dec = domain.Decision{Verdict: domain.Ask, Reason: "grant daily limit reached"}
		}
	}

	if dec.Verdict == domain.Ask {
		envBytes, _ := json.Marshal(approvedEnvelope{
			Input:         canonicalJSON(input),
			RepeatToken:   repeatToken,
			ConfirmRepeat: confirmRepeat,
		})
		apID, err := g.askApproval(ctx, c, fullName, envBytes, dec.Reason)
		if err != nil {
			return Result{Text: fmt.Sprintf("ask approval failed: %v", err), IsError: true}
		}
		return Result{
			Text: fmt.Sprintf("Cần chủ nhân duyệt (mã %s). Hãy kết thúc lượt; không thử cách khác.", apID),
		}
	}
	if dec.Verdict != domain.Allow {
		return Result{Text: fmt.Sprintf("gateway denied: %s", dec.Reason), IsError: true}
	}

	return g.executeWriteOnce(ctx, c, info, toolDef, plainCfg, secrets, input, idemKey, targetVal)
}

// ExecuteApproved executes an owner-approved ByMax gateway call at most once (blocking if task was cancelled).
func (g *Gateway) ExecuteApproved(ctx context.Context, c Caller, fullTool string, rawEnvelope json.RawMessage) Result {
	if g.isConversationTaskCancelled(ctx, c) {
		return Result{Text: "task đã bị hủy; bỏ qua thực thi tác động ra ngoài", IsError: true}
	}
	connName, toolName, ok := strings.Cut(fullTool, ".")
	if !ok {
		return Result{Text: "invalid approved tool name " + fullTool, IsError: true}
	}
	var env approvedEnvelope
	input := rawEnvelope
	if json.Unmarshal(rawEnvelope, &env) == nil && len(env.Input) > 0 {
		input = env.Input
	}
	info, toolDef, plainCfg, secrets, errRes := g.lookupConnAndTool(ctx, connName, toolName)
	if errRes != nil {
		return *errRes
	}
	idemKey := IdempotencyKey(c.ConversationID, fullTool, input, env.RepeatToken)
	targetVal := extractTargetField(input, toolDef.TargetField)
	return g.executeWriteOnce(ctx, c, info, toolDef, plainCfg, secrets, input, idemKey, targetVal)
}

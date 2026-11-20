package fakeengine

import (
	"context"
	"encoding/json"
	"sync"

	"max/internal/app"
	"max/internal/domain"
)

// Fake implements app.Engine deterministically for unit tests without spawning subprocesses.
type Fake struct {
	mu     sync.Mutex
	name   domain.EngineName
	caps   app.EngineCaps
	health app.EngineHealth
	script []app.EngineEvent
	onTool func(app.ToolAsk)
	specs  []app.RunSpec
	dyn    func(ctx context.Context, spec app.RunSpec, perm app.PermissionFunc, out chan<- app.EngineEvent)
}

// New creates a Fake engine that emits script in order and closes the event channel.
func New(name domain.EngineName, script ...app.EngineEvent) *Fake {
	return &Fake{
		name:   name,
		caps:   app.EngineCaps{PerRunMCP: true, Resume: true},
		health: app.EngineHealth{Installed: true, LoggedIn: true, Version: "fake-1.0.0"},
		script: append([]app.EngineEvent(nil), script...),
	}
}

// OnTool registers a callback invoked on every tool_call event before PermissionFunc runs.
func (f *Fake) OnTool(fn func(app.ToolAsk)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onTool = fn
}

// SetDynamic overrides static script replay with a custom per-run generator.
func (f *Fake) SetDynamic(fn func(ctx context.Context, spec app.RunSpec, perm app.PermissionFunc, out chan<- app.EngineEvent)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dyn = fn
}

// Specs returns all RunSpec invocations received so far.
func (f *Fake) Specs() []app.RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]app.RunSpec(nil), f.specs...)
}

func (f *Fake) Name() domain.EngineName { return f.name }

func (f *Fake) Caps() app.EngineCaps { return f.caps }

func (f *Fake) Health(_ context.Context) app.EngineHealth { return f.health }

func (f *Fake) Run(ctx context.Context, spec app.RunSpec, perm app.PermissionFunc) (<-chan app.EngineEvent, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	script := append([]app.EngineEvent(nil), f.script...)
	onTool := f.onTool
	dyn := f.dyn
	f.mu.Unlock()

	ch := make(chan app.EngineEvent, 16)
	go func() {
		defer close(ch)
		if dyn != nil {
			dyn(ctx, spec, perm, ch)
			return
		}
		for _, ev := range script {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if ev.Type == "tool_call" {
				raw := ev.InputPreview
				if raw == "" {
					raw = "{}"
				}
				ask := app.ToolAsk{
					RunToken: spec.RunToken,
					ToolName: ev.ToolName,
					Input:    json.RawMessage(raw),
				}
				if onTool != nil {
					onTool(ask)
				}
				if perm != nil {
					_ = perm(ctx, ask)
				}
			}
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

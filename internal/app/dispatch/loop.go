package dispatch

import (
	"context"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/limits"
	"max/internal/app/run"
	"max/internal/domain"
)

const defaultPollInterval = 2 * time.Second

// Deps bundles dependencies injected into Dispatcher.
type Deps struct {
	Store        app.Store
	Actors       *actor.Service
	Runs         *run.Service
	Slots        *limits.Slots
	Quota        *limits.Quota
	Clock        app.Clock
	PollInterval time.Duration
	OnRunDone    func(c actor.Claimed, out run.Outcome)
}

// Dispatcher claims ready actor conversations whenever engine slots are free and executes runs concurrently.
type Dispatcher struct {
	deps   Deps
	notify chan struct{}
}

// New constructs a Dispatcher.
func New(deps Deps) *Dispatcher {
	if deps.Clock == nil {
		deps.Clock = app.SystemClock{}
	}
	if deps.PollInterval <= 0 {
		deps.PollInterval = defaultPollInterval
	}
	return &Dispatcher{
		deps:   deps,
		notify: make(chan struct{}, 1),
	}
}

// Notify signals the dispatch loop that new wakeups arrived or a slot became available.
func (d *Dispatcher) Notify() {
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

// Run drives the claim-and-execute loop until ctx is cancelled, then waits for all active runs to finish.
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.deps.PollInterval)
	defer ticker.Stop()

	var wg sync.WaitGroup
	var actorSig <-chan struct{}
	if d.deps.Actors != nil {
		actorSig = d.deps.Actors.Signal()
	}

	d.tryDispatch(ctx, &wg)

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-d.notify:
			d.tryDispatch(ctx, &wg)
		case <-actorSig:
			d.tryDispatch(ctx, &wg)
		case <-ticker.C:
			d.tryDispatch(ctx, &wg)
		}
	}
}

func (d *Dispatcher) tryDispatch(ctx context.Context, wg *sync.WaitGroup) {
	if ctx.Err() != nil {
		return
	}
	free := d.deps.Slots.Free()
	hasAny := false
	for _, n := range free {
		if n > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return
	}

	admitFn := func(e domain.EngineName, p domain.Priority) domain.Admission {
		caps, used, enabled := d.deps.Slots.Snapshot()
		if !enabled[e] {
			return domain.DeferLimited
		}
		if caps[e] >= 2 && p < domain.PrioInteractive && used[e] >= caps[e]-1 {
			return domain.DeferShare
		}
		if d.deps.Quota != nil {
			return d.deps.Quota.Admit(e, p)
		}
		return domain.AdmitNow
	}

	now := d.deps.Clock.Now()
	claimed, err := d.deps.Actors.ClaimBatch(ctx, actor.Capacity{
		Free:  free,
		Admit: admitFn,
	}, now)
	if err != nil || len(claimed) == 0 {
		return
	}

	for _, c := range claimed {
		if !d.deps.Slots.AcquirePriority(c.Engine, c.Priority) {
			finCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = d.deps.Runs.Finalize(finCtx, c.RunID, run.Outcome{Status: domain.RunQuotaStopped})
			cancel()
			continue
		}

		wg.Add(1)
		go func(claimedRun actor.Claimed) {
			defer wg.Done()
			defer func() {
				d.deps.Slots.Release(claimedRun.Engine)
				d.Notify()
			}()

			out, _ := d.deps.Runs.Execute(ctx, claimedRun)
			if d.deps.Quota != nil {
				endNow := d.deps.Clock.Now()
				if out.InputTokens > 0 || out.OutputTokens > 0 {
					recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					_ = d.deps.Store.WithTx(recCtx, func(tx app.Tx) error {
						return d.deps.Quota.Record(recCtx, tx, claimedRun.Engine, claimedRun.AgentID, out.InputTokens, out.OutputTokens, endNow)
					})
					cancel()
				}
				if out.Status == domain.RunQuotaStopped {
					limCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					_ = d.deps.Quota.OnLimit(limCtx, claimedRun.Engine, out.LimitResetAt, endNow)
					cancel()
				}
			}
			if d.deps.OnRunDone != nil {
				d.deps.OnRunDone(claimedRun, out)
			}
		}(c)
	}
}

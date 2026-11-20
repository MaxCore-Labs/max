package actor

import (
	"context"
	"time"

	"max/internal/app"
)

// Recover resets in-flight runs and claimed wakeups left behind by an unclean process exit (Spec 8.2.5).
func (s *Service) Recover(ctx context.Context, now time.Time) (interrupted int, err error) {
	return s.recover(ctx, now, false)
}

// RecoverGraceful resets in-flight runs during an intentional SIGTERM shutdown, refunding attempts - 1
// so deploys never drop healthy work (Spec 8.2.3 & 8.15).
func (s *Service) RecoverGraceful(ctx context.Context, now time.Time) (interrupted int, err error) {
	return s.recover(ctx, now, true)
}

func (s *Service) recover(ctx context.Context, now time.Time, graceful bool) (int, error) {
	var count int
	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		var rerr error
		count, rerr = tx.RecoverCrashState(ctx, now.UnixMilli(), graceful, s.ids.New)
		return rerr
	})
	return count, err
}

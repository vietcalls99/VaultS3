package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/Kodiqa-Solutions/VaultS3/internal/selfupdate"
)

// runUpdateChecker polls GitHub Releases on an interval. It always records the
// result (for the dashboard notifier); it only installs an update when
// auto_update.apply is set and the deployment is a plain binary (not Docker).
func (s *Server) runUpdateChecker(u *selfupdate.Updater) {
	interval := time.Duration(s.cfg.AutoUpdate.CheckIntervalHrs) * time.Hour
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	// First check shortly after startup, then on the interval.
	time.Sleep(30 * time.Second)
	s.checkForUpdate(u)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s.checkForUpdate(u)
	}
}

func (s *Server) checkForUpdate(u *selfupdate.Updater) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	st := u.Check(ctx)
	cancel()

	if st.Error != "" {
		slog.Warn("update check failed", "error", st.Error)
		return
	}
	if !st.UpdateAvailable {
		return
	}
	slog.Info("update available", "current", st.Current, "latest", st.Latest)

	if !s.cfg.AutoUpdate.Apply {
		return // notifier-only
	}
	if selfupdate.IsDocker() {
		slog.Warn("auto-update skipped: running in Docker — update the image (e.g. with Watchtower) instead")
		return
	}

	// Download + verify + install + restart. Apply re-execs on success, so this
	// only returns on failure.
	applyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	slog.Info("auto-update: installing", "from", st.Current, "to", st.Latest)
	if err := u.Apply(applyCtx, s.cfg.AutoUpdate.AllowMajor); err != nil {
		slog.Error("auto-update failed (continuing on current version)", "error", err)
	}
}

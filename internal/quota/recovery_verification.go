package quota

import (
	"context"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"github.com/sirupsen/logrus"
)

// queueResetVerification uses the existing task map, worker tokens and lifecycle.
// An already flying request cannot count as the post-reset read: defer one new
// read until that task completes. Repeated requests coalesce into one flag.
func (s *Service) queueResetVerification(identity entities.UsageIdentity) {
	for {
		task, created := s.ensureRefreshTaskWithIdentity(identity.Identity, refreshSourceResetVerification, identity)
		if !created {
			s.refreshMu.Lock()
			if s.refreshTaskCurrentLocked(task) && task.isActive() {
				task.resetVerificationPending = true
				s.refreshMu.Unlock()
				return
			}
			s.refreshMu.Unlock()
			// The old task completed between ensure and the lock. A fresh ensure
			// is required; do not silently use its pre-barrier observation.
			continue
		}
		if !s.startRefreshGoroutine(func() { s.dispatchRefreshTasks([]*RefreshTaskRecord{task}) }) {
			s.markQueuedRefreshTasksFailed([]*RefreshTaskRecord{task}, context.Canceled)
		}
		return
	}
}

func (s *Service) dispatchPendingResetVerification(task *RefreshTaskRecord) {
	s.refreshMu.Lock()
	pending := task.resetVerificationPending
	task.resetVerificationPending = false
	s.refreshMu.Unlock()
	if !pending {
		return
	}
	ctx, cancel := context.WithTimeout(s.refreshContextSnapshot(), RefreshTaskTimeout)
	defer cancel()
	identity, err := repository.GetActiveCredentialUsageIdentityByAuthIndex(ctx, s.db, task.AuthIndex)
	if err != nil {
		logrus.WithError(err).Warn("post-reset quota verification identity lookup failed")
		return
	}
	s.queueResetVerification(identity)
}

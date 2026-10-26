package quota

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"github.com/sirupsen/logrus"
)

const recoveryWindow = 30 * time.Minute

type recoverySample struct {
	dto.QuotaRecoveryObservation
	plan string
	used float64 // Compare raw used values, without integer rounding or an extra gain threshold.
}

type recoveryBarrier struct {
	previous  *recoverySample
	uncertain bool
}

type recoveryAccountState struct {
	generation      uint64
	activeResets    int
	baseline        *recoverySample
	barrier         *recoveryBarrier
	lastCompletedAt time.Time
}

type recoveryQuery struct {
	account      string
	state        *recoveryAccountState
	generation   uint64
	startedAt    time.Time
	blocked      bool
	baselineOnly bool
}

type pendingRecoveryEvent struct {
	key   string
	event dto.QuotaRecoveryEvent
}

// weeklyRecoveryDetector is independent of the history runner and all summary
// projections. Only incidents persist; a new process always learns new baselines.
type weeklyRecoveryDetector struct {
	mu                sync.Mutex
	flushMu           sync.Mutex
	startedAt         time.Time
	accounts          map[string]*recoveryAccountState
	identities        map[string]string
	hits              map[string]dto.QuotaRecoveryEvidence
	latestCompletedAt time.Time
	muteUntil         time.Time
	pending           *pendingRecoveryEvent // At most one frozen incident while the DB is unavailable.
	now               func() time.Time
	write             func(context.Context, string, dto.QuotaRecoveryEvent) error
}

func newWeeklyRecoveryDetector(write func(context.Context, string, dto.QuotaRecoveryEvent) error) *weeklyRecoveryDetector {
	return &weeklyRecoveryDetector{
		startedAt: time.Now().UTC(), accounts: map[string]*recoveryAccountState{}, identities: map[string]string{},
		hits: map[string]dto.QuotaRecoveryEvidence{}, now: time.Now, write: write,
	}
}

func recoveryAccountRef(identity entities.UsageIdentity) string {
	if !usageHeaderIdentityIsCodex(identity) || optionalAccountID(identity.AccountID) == "" {
		return ""
	}
	// Official account IDs are never returned. Duplicate credential files share
	// this deterministic reference, including across process restarts.
	hash := sha256.Sum256([]byte("keeper:codex:weekly-recovery:\x00" + optionalAccountID(identity.AccountID)))
	return "codex_" + hex.EncodeToString(hash[:])
}

func recoveryOfficialSource(source RefreshSource) bool {
	return source == "" || codexQuotaHistorySourceIsAuthoritative(source) || source == refreshSourceResetVerification
}

// accountLocked also invalidates the old scope when a credential changes identity.
func (d *weeklyRecoveryDetector) accountLocked(identity entities.UsageIdentity) (string, *recoveryAccountState) {
	ref := recoveryAccountRef(identity)
	if old, ok := d.identities[identity.Identity]; ok && old != ref {
		if state := d.accounts[old]; state != nil {
			state.generation++
			state.baseline = nil
		}
		delete(d.hits, old)
	}
	if ref == "" {
		delete(d.identities, identity.Identity)
		return "", nil
	}
	d.identities[identity.Identity] = ref
	if d.accounts[ref] == nil {
		d.accounts[ref] = &recoveryAccountState{}
	}
	return ref, d.accounts[ref]
}

func (d *weeklyRecoveryDetector) begin(identity entities.UsageIdentity, source RefreshSource) recoveryQuery {
	if d == nil || !recoveryOfficialSource(source) {
		return recoveryQuery{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ref, state := d.accountLocked(identity)
	if state == nil {
		return recoveryQuery{}
	}
	return recoveryQuery{account: ref, state: state, generation: state.generation, startedAt: d.now().UTC(), blocked: state.activeResets > 0, baselineOnly: source == refreshSourceResetVerification}
}

func recoveryWeeklySample(output ProviderOutput, started, completed time.Time) *recoverySample {
	usage := codexUsagePayloadFromProviderOutput(output)
	if normalizeIdentityType(output.Provider) != "codex" || usage == nil || usage.RateLimit == nil {
		return nil
	}
	plan := newCodexSubscription(usage.PlanType)
	if plan == nil || strings.EqualFold(plan.Plan, "unknown") {
		return nil
	}
	var sample *recoverySample
	for _, window := range []*CodexUsageWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if window == nil || !window.HasLimitWindowSeconds || window.LimitWindowSeconds != quotaWindowSevenDaySeconds {
			continue
		}
		if sample != nil || !window.HasUsedPercent || !window.HasResetAt || window.ResetAt <= 0 ||
			math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) || window.UsedPercent < 0 || window.UsedPercent > 100 {
			return nil
		}
		sample = &recoverySample{plan: plan.Plan, used: window.UsedPercent, QuotaRecoveryObservation: dto.QuotaRecoveryObservation{
			QueryStartedAt: started.UTC(), QueryCompletedAt: completed.UTC(), RemainingFraction: 1 - window.UsedPercent/100, ResetAt: time.Unix(window.ResetAt, 0).UTC(),
		}}
	}
	return sample
}

func (d *weeklyRecoveryDetector) finish(query recoveryQuery, output ProviderOutput, completed time.Time) {
	if d == nil || query.state == nil {
		return
	}
	d.mu.Lock()
	d.observeLocked(query, output, completed.UTC())
	d.mu.Unlock()
	d.flush()
}

func (d *weeklyRecoveryDetector) observeLocked(query recoveryQuery, output ProviderOutput, completed time.Time) {
	state := d.accounts[query.account]
	if state != query.state || state.generation != query.generation || query.blocked || state.activeResets > 0 ||
		completed.Before(query.startedAt) || !query.startedAt.After(state.lastCompletedAt) {
		return
	}
	state.lastCompletedAt = completed
	if completed.After(d.latestCompletedAt) {
		d.latestCompletedAt = completed
	}
	for account, hit := range d.hits {
		if hit.After.QueryCompletedAt.Before(d.latestCompletedAt.Add(-recoveryWindow)) {
			delete(d.hits, account)
		}
	}
	sample := recoveryWeeklySample(output, query.startedAt, completed)
	if sample == nil {
		state.baseline = nil
		delete(d.hits, query.account)
		return
	}
	if barrier := state.barrier; barrier != nil {
		if !barrier.uncertain {
			state.barrier, state.baseline = nil, sample
			return
		}
		if barrier.previous == nil {
			// Without a pre-reset boundary, the first valid read supplies the
			// conservative waiting boundary. It does not prove non-consumption.
			barrier.previous = sample
			return
		}
		previous := barrier.previous
		restored := sample.plan == previous.plan && sample.used < previous.used && sample.ResetAt.Sub(previous.ResetAt) > codexQuotaHistoryResetTolerance
		if restored || !completed.Before(previous.ResetAt) {
			state.barrier, state.baseline = nil, sample
		}
		return
	}
	if query.baselineOnly {
		state.baseline = sample
		delete(d.hits, query.account)
		return
	}
	previous := state.baseline
	state.baseline = sample
	if previous == nil || previous.plan != sample.plan {
		delete(d.hits, query.account)
		return
	}
	if sample.ResetAt.Before(previous.ResetAt.Add(-codexQuotaHistoryResetTolerance)) {
		delete(d.hits, query.account)
	}
	if !completed.Before(previous.ResetAt.Add(-codexQuotaHistoryResetTolerance)) ||
		sample.ResetAt.Sub(previous.ResetAt) <= codexQuotaHistoryResetTolerance || sample.used >= previous.used ||
		completed.Before(d.muteUntil) || completed.Before(d.latestCompletedAt.Add(-recoveryWindow)) || d.pending != nil {
		return
	}
	if _, exists := d.hits[query.account]; !exists {
		d.hits[query.account] = dto.QuotaRecoveryEvidence{
			AccountRef: query.account, Plan: sample.plan, Before: previous.QuotaRecoveryObservation, After: sample.QuotaRecoveryObservation,
			RemainingIncreasePoints: previous.used - sample.used, ResetDelaySeconds: int64(sample.ResetAt.Sub(previous.ResetAt) / time.Second),
			OccurredBetween: dto.QuotaRecoveryTimeRange{From: previous.QueryStartedAt, To: completed},
		}
	}
	if len(d.hits) < 3 {
		return
	}
	hits := make([]dto.QuotaRecoveryEvidence, 0, len(d.hits))
	for _, hit := range d.hits {
		hits = append(hits, hit)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].After.QueryCompletedAt.Equal(hits[j].After.QueryCompletedAt) {
			return hits[i].AccountRef < hits[j].AccountRef
		}
		return hits[i].After.QueryCompletedAt.Before(hits[j].After.QueryCompletedAt)
	})
	hits = hits[:3]
	d.pending = &pendingRecoveryEvent{key: rand.Text(), event: dto.QuotaRecoveryEvent{
		Type: "multi_account_weekly_recovery", RuleVersion: 1, DetectorStartedAt: d.startedAt,
		FirstDetectedAt: hits[0].After.QueryCompletedAt, DetectedAt: hits[2].After.QueryCompletedAt,
		AccountCount: 3, KeeperResetExcluded: true, ExternalResetStatus: "unknown", Accounts: hits,
	}}
}

func (d *weeklyRecoveryDetector) flush() {
	if d == nil {
		return
	}
	d.flushMu.Lock()
	defer d.flushMu.Unlock()
	d.mu.Lock()
	pending := d.pending
	d.mu.Unlock()
	if pending == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := d.write(ctx, pending.key, pending.event)
	cancel()
	if err != nil {
		// Evidence and the same key stay in memory; a later official response
		// retries only this local insert, never an upstream operation.
		logrus.WithError(err).Warn("quota recovery incident persistence failed")
		return
	}
	d.mu.Lock()
	d.pending = nil
	d.hits = map[string]dto.QuotaRecoveryEvidence{}
	d.muteUntil = d.now().UTC().Add(recoveryWindow)
	d.mu.Unlock()
}

func (d *weeklyRecoveryDetector) beginReset(identity entities.UsageIdentity) recoveryQuery {
	if d == nil {
		return recoveryQuery{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	ref, state := d.accountLocked(identity)
	if state == nil {
		return recoveryQuery{}
	}
	if state.barrier == nil {
		state.barrier = &recoveryBarrier{previous: state.baseline}
	}
	state.generation++
	state.activeResets++
	state.baseline = nil
	delete(d.hits, ref)
	return recoveryQuery{account: ref, state: state}
}

func (d *weeklyRecoveryDetector) endReset(ticket recoveryQuery, known bool) {
	if d == nil || ticket.state == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	state := ticket.state
	state.generation++ // Excludes every read started before completion, including duplicate files.
	state.activeResets--
	if !known {
		state.barrier.uncertain = true
	}
}

// GetQuotaRecoveryEvents is a passive read: it neither flushes pending incidents
// nor consults/advances baselines or provider refreshes.
func (s *Service) GetQuotaRecoveryEvents(ctx context.Context, afterID int64, limit int) (dto.QuotaRecoveryEventsPage, error) {
	var started time.Time
	if s.recovery != nil {
		started = s.recovery.startedAt
	}
	return repository.ReadQuotaRecoveryEvents(ctx, s.db, afterID, limit, started)
}

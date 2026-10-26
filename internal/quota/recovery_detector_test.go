package quota

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository/dto"
)

func recoveryTestIdentity(i int) entities.UsageIdentity {
	account := fmt.Sprintf("private-account-%d", i)
	return entities.UsageIdentity{Identity: fmt.Sprintf("private-credential-%d", i), Type: "codex", Provider: "codex", AuthType: entities.UsageIdentityAuthTypeCredential, AccountID: &account}
}

func recoveryTestOutput(used float64, reset time.Time) ProviderOutput {
	return ProviderOutput{Provider: "codex", Result: CodexResult{Usage: &CodexUsagePayload{PlanType: "team", RateLimit: &CodexRateLimitInfo{
		SecondaryWindow: &CodexUsageWindow{UsedPercent: used, HasUsedPercent: true, LimitWindowSeconds: 604800, HasLimitWindowSeconds: true, ResetAt: reset.Unix(), HasResetAt: true},
	}}}}
}

func recoveryTestDetector(t *testing.T) (*weeklyRecoveryDetector, *time.Time, *[]dto.QuotaRecoveryEvent) {
	t.Helper()
	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	events := []dto.QuotaRecoveryEvent{}
	d := newWeeklyRecoveryDetector(func(_ context.Context, _ string, event dto.QuotaRecoveryEvent) error {
		events = append(events, event)
		return nil
	})
	d.now = func() time.Time { return clock }
	return d, &clock, &events
}

func recoveryTestObserve(d *weeklyRecoveryDetector, clock *time.Time, identity entities.UsageIdentity, at time.Time, output ProviderOutput) {
	*clock = at
	query := d.begin(identity, RefreshSourceScheduled)
	*clock = at.Add(time.Second)
	d.finish(query, output, *clock)
}

func TestRecoveryTwoObservationsThreeAccountsAndSilence(t *testing.T) {
	d, clock, events := recoveryTestDetector(t)
	start := *clock
	oldReset := start.Add(5 * 24 * time.Hour)
	for i := 0; i < 6; i++ {
		recoveryTestObserve(d, clock, recoveryTestIdentity(i), start.Add(time.Duration(i)*time.Second), recoveryTestOutput(50, oldReset))
	}
	for i := 0; i < 3; i++ {
		// Different reset times and a tiny real increase must still count.
		recoveryTestObserve(d, clock, recoveryTestIdentity(i), start.Add(time.Hour+time.Duration(i)*time.Minute), recoveryTestOutput(49.9999, oldReset.Add(time.Duration(24+i)*time.Hour)))
		if i < 2 && len(*events) != 0 {
			t.Fatal("single/two account hit published")
		}
	}
	if len(*events) != 1 || (*events)[0].AccountCount != 3 || len((*events)[0].Accounts) != 3 {
		t.Fatalf("third account did not publish immediately: %+v", *events)
	}
	event := (*events)[0]
	if !event.DetectedAt.Equal(start.Add(62*time.Minute+time.Second)) || !event.FirstDetectedAt.Equal(start.Add(time.Hour+time.Second)) {
		t.Fatal("detection times do not represent the actual queries")
	}
	for _, evidence := range event.Accounts {
		if evidence.OccurredBetween.From != evidence.Before.QueryStartedAt || evidence.OccurredBetween.To != evidence.After.QueryCompletedAt || evidence.RemainingIncreasePoints <= 0 {
			t.Fatal("evidence lost precision or actual boundaries")
		}
	}
	for i := 3; i < 6; i++ {
		recoveryTestObserve(d, clock, recoveryTestIdentity(i), start.Add(65*time.Minute+time.Duration(i)*time.Second), recoveryTestOutput(10, oldReset.Add(48*time.Hour)))
	}
	if len(*events) != 1 {
		t.Fatal("same-wave followups duplicated the incident")
	}
}

func TestRecoveryRejectsNonFactsAndNormalTransitions(t *testing.T) {
	for _, kind := range []string{"same amount", "more used", "same reset", "120 second drift", "natural reset", "near natural reset", "missing absolute", "invalid used", "nonfinite", "wrong window", "changed plan", "missing plan", "duplicate weekly", "projection"} {
		t.Run(kind, func(t *testing.T) {
			d, clock, events := recoveryTestDetector(t)
			start, reset := *clock, clock.Add(5*24*time.Hour)
			for i := 0; i < 3; i++ {
				identity := recoveryTestIdentity(i)
				recoveryTestObserve(d, clock, identity, start, recoveryTestOutput(50, reset))
				at, output := start.Add(time.Hour), recoveryTestOutput(10, reset.Add(48*time.Hour))
				usage := output.Result.(CodexResult).Usage
				window := usage.RateLimit.SecondaryWindow
				switch kind {
				case "same amount":
					window.UsedPercent = 50
				case "more used":
					window.UsedPercent = 51
				case "same reset":
					window.ResetAt = reset.Unix()
				case "120 second drift":
					window.ResetAt = reset.Add(120 * time.Second).Unix()
				case "natural reset":
					at = reset
				case "near natural reset":
					at = reset.Add(-120 * time.Second)
				case "missing absolute":
					window.HasResetAt = false
					window.HasResetAfterSeconds = true
				case "invalid used":
					window.UsedPercent = -1
				case "nonfinite":
					window.UsedPercent = math.NaN()
				case "wrong window":
					window.LimitWindowSeconds = 18000
				case "changed plan":
					usage.PlanType = "plus"
				case "missing plan":
					usage.PlanType = ""
				case "duplicate weekly":
					usage.RateLimit.PrimaryWindow = window
				case "projection":
					output.Result = CheckResponse{Quota: []QuotaRow{{RemainingFraction: new(1.0)}}}
				}
				recoveryTestObserve(d, clock, identity, at, output)
			}
			if len(*events) != 0 || len(d.hits) != 0 {
				t.Fatalf("%s produced a hit", kind)
			}
		})
	}
}

func TestRecoveryWindowIdentityAndColdStart(t *testing.T) {
	d, clock, events := recoveryTestDetector(t)
	start, reset := *clock, clock.Add(5*24*time.Hour)
	for i := 0; i < 3; i++ {
		identity := recoveryTestIdentity(i)
		recoveryTestObserve(d, clock, identity, start, recoveryTestOutput(50, reset))
		at := start.Add(time.Hour + time.Duration(i)*16*time.Minute)
		recoveryTestObserve(d, clock, identity, at, recoveryTestOutput(10, reset.Add(48*time.Hour)))
	}
	if len(*events) != 0 {
		t.Fatal("hits more than 30 minutes apart merged")
	}
	d, clock, events = recoveryTestDetector(t)
	for i := 0; i < 3; i++ {
		identity := recoveryTestIdentity(0)
		identity.Identity = fmt.Sprintf("duplicate-file-%d", i)
		recoveryTestObserve(d, clock, identity, start.Add(time.Duration(i)*time.Second), recoveryTestOutput(50, reset))
		recoveryTestObserve(d, clock, identity, start.Add(time.Hour+time.Duration(i)*time.Minute), recoveryTestOutput(10-float64(i), reset.Add(time.Duration(48+i)*time.Hour)))
	}
	if len(*events) != 0 || len(d.hits) != 1 {
		t.Fatal("credential copies counted as different accounts")
	}
	d, clock, events = recoveryTestDetector(t)
	for i := 0; i < 3; i++ {
		identity := recoveryTestIdentity(i)
		identity.AccountID = nil
		recoveryTestObserve(d, clock, identity, start, recoveryTestOutput(50, reset))
		recoveryTestObserve(d, clock, identity, start.Add(time.Hour), recoveryTestOutput(10, reset.Add(48*time.Hour)))
	}
	if len(*events) != 0 || len(d.hits) != 0 {
		t.Fatal("missing reliable identity counted")
	}
	// A new process must not compare with persisted historical percentages.
	d, clock, events = recoveryTestDetector(t)
	for i := 0; i < 3; i++ {
		recoveryTestObserve(d, clock, recoveryTestIdentity(i), start, recoveryTestOutput(0, reset.Add(48*time.Hour)))
	}
	if len(*events) != 0 {
		t.Fatal("cold start manufactured a recovery")
	}
}

func TestRecoveryResetBarriersAndCrossingRequests(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			d, clock, events := recoveryTestDetector(t)
			start, reset := *clock, clock.Add(5*24*time.Hour)
			identity := recoveryTestIdentity(1)
			recoveryTestObserve(d, clock, identity, start, recoveryTestOutput(50, reset))
			*clock = start.Add(time.Minute)
			oldFlight := d.begin(identity, RefreshSourceScheduled)
			ticket := d.beginReset(identity)
			duplicate := identity
			duplicate.Identity = "other-file-same-account"
			during := d.begin(duplicate, RefreshSourceManual)
			d.endReset(ticket, known)
			d.finish(oldFlight, recoveryTestOutput(0, reset.Add(48*time.Hour)), start.Add(2*time.Minute))
			d.finish(during, recoveryTestOutput(0, reset.Add(48*time.Hour)), start.Add(3*time.Minute))
			state := d.accounts[ticket.account]
			if state.baseline != nil || state.barrier == nil {
				t.Fatal("cross-barrier response resurrected baseline")
			}
			*clock = start.Add(4 * time.Minute)
			verification := d.begin(duplicate, refreshSourceResetVerification)
			verificationOutput := recoveryTestOutput(50, reset)
			if known {
				verificationOutput = recoveryTestOutput(10, reset.Add(48*time.Hour))
			}
			d.finish(verification, verificationOutput, clock.Add(time.Second))
			if !known && state.barrier == nil {
				t.Fatal("unchanged read was treated as non-consumption")
			}
			recoveryTestObserve(d, clock, duplicate, start.Add(5*time.Minute), recoveryTestOutput(10, reset.Add(48*time.Hour)))
			if state.barrier != nil || state.baseline == nil || len(d.hits) != 0 || len(*events) != 0 {
				t.Fatal("manual recovery was not silently rebased")
			}
		})
	}
}

func TestRecoveryUnknownWithoutBaselineAndNaturalBoundary(t *testing.T) {
	d, clock, _ := recoveryTestDetector(t)
	identity := recoveryTestIdentity(1)
	start, reset := *clock, clock.Add(5*24*time.Hour)
	ticket := d.beginReset(identity)
	d.endReset(ticket, false)
	recoveryTestObserve(d, clock, identity, start.Add(time.Hour), recoveryTestOutput(10, reset))
	if d.accounts[ticket.account].barrier == nil {
		t.Fatal("unknown without old baseline was cleared immediately")
	}
	recoveryTestObserve(d, clock, identity, reset, recoveryTestOutput(0, reset.Add(7*24*time.Hour)))
	if d.accounts[ticket.account].barrier != nil || len(d.hits) != 0 {
		t.Fatal("natural boundary did not silently end barrier")
	}
}

func TestRecoveryOutOfOrderSourceAndRetry(t *testing.T) {
	d, clock, events := recoveryTestDetector(t)
	start, reset := *clock, clock.Add(5*24*time.Hour)
	identity := recoveryTestIdentity(1)
	recoveryTestObserve(d, clock, identity, start, recoveryTestOutput(50, reset))
	*clock = start.Add(time.Minute)
	older := d.begin(identity, RefreshSourceManual)
	*clock = start.Add(2 * time.Minute)
	newer := d.begin(identity, RefreshSourceManual)
	d.finish(newer, recoveryTestOutput(45, reset), start.Add(3*time.Minute))
	d.finish(older, recoveryTestOutput(0, reset.Add(48*time.Hour)), start.Add(4*time.Minute))
	header := d.begin(identity, RefreshSourceUsageHeader)
	d.finish(header, recoveryTestOutput(0, reset.Add(48*time.Hour)), start.Add(5*time.Minute))
	if len(d.hits) != 0 {
		t.Fatal("late/header response counted")
	}
	var keys []string
	d.write = func(_ context.Context, key string, event dto.QuotaRecoveryEvent) error {
		keys = append(keys, key)
		if len(keys) == 1 {
			return errors.New("simulated lost acknowledgement")
		}
		*events = append(*events, event)
		return nil
	}
	for i := 2; i < 5; i++ {
		recoveryTestObserve(d, clock, recoveryTestIdentity(i), start.Add(10*time.Minute), recoveryTestOutput(50, reset))
		recoveryTestObserve(d, clock, recoveryTestIdentity(i), start.Add(20*time.Minute), recoveryTestOutput(0, reset.Add(time.Duration(48+i)*time.Hour)))
	}
	if d.pending == nil || len(*events) != 0 {
		t.Fatal("failed event not retained")
	}
	frozen := d.pending.event
	recoveryTestObserve(d, clock, identity, start.Add(21*time.Minute), recoveryTestOutput(45, reset))
	if d.pending != nil || len(keys) != 2 || keys[0] != keys[1] || len(*events) != 1 || !(*events)[0].DetectedAt.Equal(frozen.DetectedAt) {
		t.Fatal("retry changed or duplicated evidence")
	}
}

func TestRecoveryConcurrentDuplicateFinishes(t *testing.T) {
	var written int
	d := newWeeklyRecoveryDetector(func(context.Context, string, dto.QuotaRecoveryEvent) error { written++; return nil })
	start := time.Now().Add(-time.Hour)
	reset := time.Now().Add(5 * 24 * time.Hour)
	queries := make([]recoveryQuery, 3)
	for i := 0; i < 3; i++ {
		state := &recoveryAccountState{baseline: &recoverySample{plan: "team", used: 50, QuotaRecoveryObservation: dto.QuotaRecoveryObservation{QueryStartedAt: start, QueryCompletedAt: start.Add(time.Second), ResetAt: reset}}}
		ref := recoveryAccountRef(recoveryTestIdentity(i))
		d.accounts[ref] = state
		queries[i] = recoveryQuery{account: ref, state: state, startedAt: time.Now()}
	}
	var wg sync.WaitGroup
	for _, q := range queries {
		for j := 0; j < 3; j++ {
			wg.Add(1)
			go func() { defer wg.Done(); d.finish(q, recoveryTestOutput(10, reset.Add(48*time.Hour)), time.Now()) }()
		}
	}
	wg.Wait()
	if written != 1 {
		t.Fatalf("duplicate concurrent records: %d", written)
	}
}

func TestRecoveryDelayedCrossAccountCompletionCannotReviveExpiredHit(t *testing.T) {
	d, clock, events := recoveryTestDetector(t)
	start, reset := *clock, clock.Add(5*24*time.Hour)
	for i := 0; i < 3; i++ {
		recoveryTestObserve(d, clock, recoveryTestIdentity(i), start, recoveryTestOutput(50, reset))
	}
	// Account 0 completed much earlier but its detector callback was delayed.
	*clock = start.Add(time.Minute)
	delayed := d.begin(recoveryTestIdentity(0), RefreshSourceScheduled)
	for i := 1; i < 3; i++ {
		recoveryTestObserve(d, clock, recoveryTestIdentity(i), start.Add(time.Hour+time.Duration(i)*time.Minute), recoveryTestOutput(10, reset.Add(48*time.Hour)))
	}
	d.finish(delayed, recoveryTestOutput(10, reset.Add(48*time.Hour)), start.Add(2*time.Minute))
	if len(*events) != 0 || len(d.hits) != 2 {
		t.Fatalf("expired delayed hit resurrected: %+v", *events)
	}
}

package test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	. "cpa-usage-keeper/internal/quota"
)

func officialWeekly(used float64, reset time.Time) ProviderOutput {
	return ProviderOutput{Provider: "codex", Result: CodexResult{Usage: &CodexUsagePayload{PlanType: "team", RateLimit: &CodexRateLimitInfo{SecondaryWindow: &CodexUsageWindow{UsedPercent: used, HasUsedPercent: true, LimitWindowSeconds: 604800, HasLimitWindowSeconds: true, ResetAt: reset.Unix(), HasResetAt: true}}}}}
}

func TestWeeklyRecoveryServicePersistsAndRestartLearns(t *testing.T) {
	db := openQuotaTestDatabase(t)
	oldReset := time.Now().Add(5 * 24 * time.Hour)
	handler := &recordingProviderHandler{output: officialWeekly(50, oldReset)}
	registry := NewProviderRegistry(map[string]ProviderHandler{"codex": handler})
	svc := newQuotaServiceWithRegistry(t, db, registry)
	for i := 0; i < 3; i++ {
		seedUsageIdentity(t, db, entities.UsageIdentity{Identity: fmt.Sprintf("credential-%d", i), Provider: "codex", Type: "codex", AuthType: entities.UsageIdentityAuthTypeCredential, AccountID: new(fmt.Sprintf("sensitive-account-%d", i))})
		if _, err := svc.Check(context.Background(), CheckRequest{AuthIndex: fmt.Sprintf("credential-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	handler.output = officialWeekly(40, oldReset.Add(48*time.Hour))
	for i := 0; i < 3; i++ {
		if _, err := svc.Check(context.Background(), CheckRequest{AuthIndex: fmt.Sprintf("credential-%d", i), Source: RefreshSourceScheduled}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(handler.inputs)
	page, err := svc.GetQuotaRecoveryEvents(context.Background(), 0, 50)
	if err != nil || len(page.Events) != 1 || page.Events[0].AccountCount != 3 {
		t.Fatalf("events %+v %v", page, err)
	}
	for _, e := range page.Events[0].Accounts {
		if strings.Contains(e.AccountRef, "sensitive") || e.RemainingIncreasePoints != 10 {
			t.Fatalf("evidence %+v", e)
		}
	}
	if len(handler.inputs) != before {
		t.Fatal("passive event read refreshed provider")
	}
	svc.StopRefreshTasks()
	restarted := newQuotaServiceWithRegistry(t, db, registry)
	handler.output = officialWeekly(0, oldReset.Add(96*time.Hour))
	for i := 0; i < 3; i++ {
		if _, err := restarted.Check(context.Background(), CheckRequest{AuthIndex: fmt.Sprintf("credential-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err = restarted.GetQuotaRecoveryEvents(context.Background(), 0, 50)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("restart replayed history %+v %v", page, err)
	}
}

type recoveryVerificationHandler struct {
	mu               sync.Mutex
	checks, consumes int
	firstStarted     chan struct{}
	releaseFirst     chan struct{}
	verification     chan struct{}
	output           ProviderOutput
	resetErr         error
	resetOutput      ProviderResetOutput
}

func (h *recoveryVerificationHandler) Check(ctx context.Context, _ ProviderInput) (ProviderOutput, error) {
	h.mu.Lock()
	h.checks++
	call := h.checks
	h.mu.Unlock()
	if call == 1 && h.firstStarted != nil {
		close(h.firstStarted)
		select {
		case <-h.releaseFirst:
		case <-ctx.Done():
			return ProviderOutput{}, ctx.Err()
		}
	}
	if (h.firstStarted == nil || call > 1) && h.verification != nil {
		select {
		case h.verification <- struct{}{}:
		default:
		}
	}
	return h.output, nil
}
func (h *recoveryVerificationHandler) Reset(context.Context, ProviderInput) (ProviderResetOutput, error) {
	h.mu.Lock()
	h.consumes++
	h.mu.Unlock()
	return h.resetOutput, h.resetErr
}
func TestWeeklyRecoveryUnknownResetOneReadonlyVerification(t *testing.T) {
	for _, oldFlight := range []bool{false, true} {
		t.Run(fmt.Sprint(oldFlight), func(t *testing.T) {
			db := openQuotaTestDatabase(t)
			seedUsageIdentity(t, db, entities.UsageIdentity{Identity: "reset-account", Provider: "codex", Type: "codex", AuthType: entities.UsageIdentityAuthTypeCredential, AccountID: new("reliable-account")})
			h := &recoveryVerificationHandler{output: officialWeekly(50, time.Now().Add(5*24*time.Hour)), resetErr: context.DeadlineExceeded, verification: make(chan struct{}, 2)}
			if oldFlight {
				h.firstStarted = make(chan struct{})
				h.releaseFirst = make(chan struct{})
				defer closeIfOpen(h.releaseFirst)
			}
			svc := newQuotaRefreshService(t, db, NewProviderRegistry(map[string]ProviderHandler{"codex": h}))
			if oldFlight {
				queueManualQuotaRefresh(t, svc, "reset-account")
				select {
				case <-h.firstStarted:
				case <-time.After(5 * time.Second):
					t.Fatal("old read not started")
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := svc.Reset(ctx, ResetRequest{AuthIndex: "reset-account"}); err == nil {
				// Cancelled DB lookup cannot consume; retry with an active request.
				t.Fatal("cancelled reset unexpectedly succeeded")
			}
			if _, err := svc.Reset(context.Background(), ResetRequest{AuthIndex: "reset-account"}); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if oldFlight {
				close(h.releaseFirst)
			}
			select {
			case <-h.verification:
			case <-time.After(5 * time.Second):
				t.Fatal("post-reset verification missing")
			}
			svc.WaitRefreshTasks()
			h.mu.Lock()
			defer h.mu.Unlock()
			want := 1
			if oldFlight {
				want = 2
			}
			if h.checks != want || h.consumes != 1 {
				t.Fatalf("checks=%d consumes=%d", h.checks, h.consumes)
			}
		})
	}
}
func closeIfOpen(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestWeeklyRecoveryCPAFailureDoesNotScheduleUnknownRead(t *testing.T) {
	db := openQuotaTestDatabase(t)
	seedUsageIdentity(t, db, entities.UsageIdentity{Identity: "reset-account", Provider: "codex", Type: "codex", AuthType: entities.UsageIdentityAuthTypeCredential, AccountID: new("reliable-account")})
	h := &recoveryVerificationHandler{resetOutput: ProviderResetOutput{Code: "reset", RecoveryFailed: true}}
	svc := newQuotaRefreshService(t, db, NewProviderRegistry(map[string]ProviderHandler{"codex": h}))
	response, err := svc.Reset(context.Background(), ResetRequest{AuthIndex: "reset-account"})
	if err != nil || !response.RecoveryFailed {
		t.Fatalf("%+v %v", response, err)
	}
	svc.WaitRefreshTasks()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.checks != 0 || h.consumes != 1 {
		t.Fatalf("checks=%d consumes=%d", h.checks, h.consumes)
	}
}

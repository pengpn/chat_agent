package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/repository"
)

// fakeQuotaRepository 记录预扣/结算/补偿调用，并可强制返回准入错误。
type fakeQuotaRepository struct {
	reserveCalls    []quotaCall
	settleCalls     []quotaCall
	releaseCalls    []quotaCall
	forceError      error
	dailyUsed       int64
	monthlyUsed     int64
	dailyOverride   int64
	monthlyOverride int64
}

type quotaCall struct {
	userID string
	first  int64
	second int64
}

func (f *fakeQuotaRepository) Reserve(_ context.Context, userID string, tokens int64, limits repository.QuotaLimits, maxConcurrent int, _ time.Time) error {
	if f.forceError != nil {
		return f.forceError
	}
	dailyLimit := limits.DefaultDailyTokens
	if f.dailyOverride > 0 {
		dailyLimit = f.dailyOverride
	}
	if f.dailyUsed+tokens > dailyLimit {
		return domain.ErrDailyQuotaExceeded
	}
	f.dailyUsed += tokens
	f.monthlyUsed += tokens
	f.reserveCalls = append(f.reserveCalls, quotaCall{userID: userID, first: tokens})
	return nil
}

func (f *fakeQuotaRepository) Settle(_ context.Context, userID string, reserved, actual int64, _ time.Time) error {
	f.dailyUsed += actual - reserved
	f.monthlyUsed += actual - reserved
	f.settleCalls = append(f.settleCalls, quotaCall{userID: userID, first: reserved, second: actual})
	return nil
}

func (f *fakeQuotaRepository) Release(_ context.Context, userID string, reserved int64, _ time.Time) error {
	f.dailyUsed -= reserved
	f.monthlyUsed -= reserved
	f.releaseCalls = append(f.releaseCalls, quotaCall{userID: userID, first: reserved})
	return nil
}

func (f *fakeQuotaRepository) Status(_ context.Context, userID string, limits repository.QuotaLimits, _ time.Time) (model.UserQuota, error) {
	return model.UserQuota{
		UserID: userID, DailyTokenLimit: f.dailyOverride, MonthlyTokenLimit: f.monthlyOverride,
		DailyUsedTokens: f.dailyUsed, MonthlyUsedTokens: f.monthlyUsed,
	}, nil
}

func (f *fakeQuotaRepository) SetLimits(_ context.Context, userID string, daily, monthly int64, _ time.Time) (model.UserQuota, error) {
	f.dailyOverride = daily
	f.monthlyOverride = monthly
	return model.UserQuota{UserID: userID, DailyTokenLimit: daily, MonthlyTokenLimit: monthly}, nil
}

func newTestQuota(repo repository.QuotaRepository, enabled bool) *QuotaService {
	return NewQuota(repo, QuotaConfig{
		Enabled: enabled, DefaultDailyTokens: 1_000, DefaultMonthlyTokens: 10_000,
		MaxConcurrentGenerations: 2,
	})
}

func TestQuotaReserveMapsDomainErrors(t *testing.T) {
	cases := []struct {
		forced error
		want   error
	}{
		{domain.ErrDailyQuotaExceeded, ErrDailyQuotaExceeded},
		{domain.ErrMonthlyQuotaExceeded, ErrMonthlyQuotaExceeded},
		{domain.ErrTooManyConcurrent, ErrTooManyConcurrentGenerations},
	}
	for _, testCase := range cases {
		repo := &fakeQuotaRepository{forceError: testCase.forced}
		quotas := newTestQuota(repo, true)
		if err := quotas.Reserve(context.Background(), "user-1", 100); !errors.Is(err, testCase.want) {
			t.Fatalf("forced %v: err = %v, want %v", testCase.forced, err, testCase.want)
		}
	}
}

func TestQuotaDisabledPassesEverything(t *testing.T) {
	repo := &fakeQuotaRepository{}
	quotas := newTestQuota(repo, false)
	if err := quotas.Reserve(context.Background(), "user-1", 1_000_000); err != nil {
		t.Fatal(err)
	}
	if err := quotas.Settle(context.Background(), "user-1", 100, 50); err != nil {
		t.Fatal(err)
	}
	if len(repo.reserveCalls)+len(repo.settleCalls) != 0 {
		t.Fatalf("disabled quota must not touch the repository: %#v", repo)
	}
}

func TestQuotaSettleReplacesReservedWithActual(t *testing.T) {
	repo := &fakeQuotaRepository{}
	quotas := newTestQuota(repo, true)
	if err := quotas.Reserve(context.Background(), "user-1", 300); err != nil {
		t.Fatal(err)
	}
	if repo.dailyUsed != 300 {
		t.Fatalf("daily used after reserve = %d", repo.dailyUsed)
	}
	if err := quotas.Settle(context.Background(), "user-1", 300, 120); err != nil {
		t.Fatal(err)
	}
	if repo.dailyUsed != 120 {
		t.Fatalf("daily used after settle = %d, want 120", repo.dailyUsed)
	}
}

func TestQuotaReleaseCompensatesFailedRequest(t *testing.T) {
	repo := &fakeQuotaRepository{}
	quotas := newTestQuota(repo, true)
	if err := quotas.Reserve(context.Background(), "user-1", 300); err != nil {
		t.Fatal(err)
	}
	if err := quotas.Release(context.Background(), "user-1", 300); err != nil {
		t.Fatal(err)
	}
	if repo.dailyUsed != 0 || len(repo.releaseCalls) != 1 {
		t.Fatalf("daily used = %d, release calls = %#v", repo.dailyUsed, repo.releaseCalls)
	}
}

func TestQuotaStatusMergesOverrides(t *testing.T) {
	repo := &fakeQuotaRepository{dailyOverride: 0, dailyUsed: 42}
	quotas := newTestQuota(repo, true)
	status, err := quotas.Status(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.DailyLimit != 1_000 || status.DailyUsed != 42 || status.MaxConcurrent != 2 {
		t.Fatalf("status = %#v", status)
	}
	if _, err := quotas.SetLimits(context.Background(), "user-1", 5_000, -1); err != nil {
		t.Fatal(err)
	}
	status, err = quotas.Status(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.DailyLimit != 5_000 || status.MonthlyLimit != -1 {
		t.Fatalf("status after override = %#v", status)
	}
}

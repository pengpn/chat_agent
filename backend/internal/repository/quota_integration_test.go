package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var quotaTestLimits = QuotaLimits{DefaultDailyTokens: 1_000, DefaultMonthlyTokens: 10_000}

func TestPostgresQuotaReserveSettleRelease(t *testing.T) {
	repo, userID, cleanup := openQuotaTestRepository(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	if err := repo.Reserve(ctx, userID, 400, quotaTestLimits, 0, now); err != nil {
		t.Fatal(err)
	}
	status, err := repo.Status(ctx, userID, quotaTestLimits, now)
	if err != nil || status.DailyUsedTokens != 400 || status.MonthlyUsedTokens != 400 {
		t.Fatalf("status = %#v, err = %v", status, err)
	}
	// 结算把预扣替换为实际用量。
	if err := repo.Settle(ctx, userID, 400, 150, now); err != nil {
		t.Fatal(err)
	}
	status, err = repo.Status(ctx, userID, quotaTestLimits, now)
	if err != nil || status.DailyUsedTokens != 150 {
		t.Fatalf("status after settle = %#v, err = %v", status, err)
	}
	// 补偿把用量钳制到非负数。
	if err := repo.Release(ctx, userID, 500, now); err != nil {
		t.Fatal(err)
	}
	status, err = repo.Status(ctx, userID, quotaTestLimits, now)
	if err != nil || status.DailyUsedTokens != 0 {
		t.Fatalf("status after release = %#v, err = %v", status, err)
	}
}

func TestPostgresQuotaEnforcesLimitsAndOverrides(t *testing.T) {
	repo, userID, cleanup := openQuotaTestRepository(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	if err := repo.Reserve(ctx, userID, 900, quotaTestLimits, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reserve(ctx, userID, 200, quotaTestLimits, 0, now); !errors.Is(err, domain.ErrDailyQuotaExceeded) {
		t.Fatalf("err = %v, want ErrDailyQuotaExceeded", err)
	}
	// 管理员覆盖：更大的显式额度。
	if _, err := repo.SetLimits(ctx, userID, 5_000, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reserve(ctx, userID, 200, quotaTestLimits, 0, now); err != nil {
		t.Fatal(err)
	}
	// 管理员覆盖：不限量。日、月需要同时放开，否则另一方向仍会拦截。
	if _, err := repo.SetLimits(ctx, userID, model.QuotaOverrideUnlimited, model.QuotaOverrideUnlimited, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reserve(ctx, userID, 1_000_000, quotaTestLimits, 0, now); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresQuotaRollsWindows(t *testing.T) {
	repo, userID, cleanup := openQuotaTestRepository(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	if err := repo.Reserve(ctx, userID, 400, quotaTestLimits, 0, now); err != nil {
		t.Fatal(err)
	}
	// 跨自然日后用量归零，无需后台任务。
	nextDay := now.Add(25 * time.Hour)
	status, err := repo.Status(ctx, userID, quotaTestLimits, nextDay)
	if err != nil || status.DailyUsedTokens != 0 {
		t.Fatalf("status next day = %#v, err = %v", status, err)
	}
}

func TestPostgresQuotaCountsStreamingMessagesForConcurrency(t *testing.T) {
	db, repo, userID, cleanup := openQuotaTestRepositoryWithDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	conversation := model.Conversation{UserID: userID, Title: "concurrency"}
	if err := db.WithContext(ctx).Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		message := model.Message{ConversationID: conversation.ID, Role: "assistant", Status: "streaming"}
		if err := db.WithContext(ctx).Create(&message).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 两条 streaming 消息下，上限 2 仍允许（当前请求自身也计入），上限 1 拒绝。
	if err := repo.Reserve(ctx, userID, 10, quotaTestLimits, 2, now); err != nil {
		t.Fatalf("reserve with limit 2: %v", err)
	}
	if err := repo.Release(ctx, userID, 10, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reserve(ctx, userID, 10, quotaTestLimits, 1, now); !errors.Is(err, domain.ErrTooManyConcurrent) {
		t.Fatalf("err = %v, want ErrTooManyConcurrent", err)
	}
}

func openQuotaTestRepository(t *testing.T) (QuotaRepository, string, func()) {
	db, repo, userID, cleanup := openQuotaTestRepositoryWithDB(t)
	_ = db
	return repo, userID, cleanup
}

func openQuotaTestRepositoryWithDB(t *testing.T) (*gorm.DB, QuotaRepository, string, func()) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Email:        fmt.Sprintf("quota-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "test-only", DisplayName: "Quota Test", Role: model.UserRoleUser,
	}
	if err := db.WithContext(ctx).Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		db.WithContext(ctx).Where("user_id = ?", user.ID).Delete(&model.UserQuota{})
		db.WithContext(ctx).Where("conversation_id IN (?)", db.Model(&model.Conversation{}).Select("id").Where("user_id = ?", user.ID)).Delete(&model.Message{})
		db.WithContext(ctx).Where("user_id = ?", user.ID).Delete(&model.Conversation{})
		db.WithContext(ctx).Delete(&user)
	}
	return db, newQuotaRepository(db), user.ID, cleanup
}

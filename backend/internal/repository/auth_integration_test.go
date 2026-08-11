package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresRefreshRotationAndAccountCascade(t *testing.T) {
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
	repo := New(db)
	rawRefresh, _ := auth.NewOpaqueToken()
	user := model.User{Email: fmt.Sprintf("auth-lifecycle-%d@example.com", time.Now().UnixNano()), PasswordHash: "test-only", DisplayName: "Auth Lifecycle"}
	session := model.AuthSession{
		ID: uuid.NewString(), RefreshTokenHash: auth.HashOpaqueToken(rawRefresh), DeviceName: "integration test",
		ExpiresAt: time.Now().Add(time.Hour), LastUsedAt: time.Now(),
	}
	if err := repo.Auth.CreateUserAndSession(ctx, &user, &session); err != nil {
		t.Fatal(err)
	}
	defer db.WithContext(ctx).Delete(&user)

	nextRefresh, _ := auth.NewOpaqueToken()
	rotated, err := repo.Auth.RotateSession(ctx, auth.HashOpaqueToken(rawRefresh), auth.HashOpaqueToken(nextRefresh), time.Now())
	if err != nil || rotated.ID != session.ID {
		t.Fatalf("RotateSession() session=%+v err=%v", rotated, err)
	}
	if _, err := repo.Auth.RotateSession(ctx, auth.HashOpaqueToken(rawRefresh), "unused", time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old refresh token error = %v, want ErrNotFound", err)
	}

	verification := model.AccountToken{
		UserID: user.ID, Purpose: model.AccountTokenEmailVerification,
		TokenHash: auth.HashOpaqueToken("verify-once"), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Auth.CreateAccountToken(ctx, &verification); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Auth.VerifyEmailWithToken(ctx, verification.TokenHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Auth.VerifyEmailWithToken(ctx, verification.TokenHash, time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reused verification token error = %v, want ErrNotFound", err)
	}

	if err := repo.Auth.DeleteAccount(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	for table, value := range map[string]any{
		"auth_sessions": &model.AuthSession{}, "account_tokens": &model.AccountToken{},
	} {
		var count int64
		if err := db.Model(value).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows after account deletion = %d", table, count)
		}
	}
}

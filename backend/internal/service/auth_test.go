package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/mailer"
	"github.com/example/agent-platform/backend/internal/ratelimit"
	"github.com/example/agent-platform/backend/internal/repository"
)

// authRepositoryStub embeds the interface so each test only implements the
// persistence operations exercised by that scenario.
type authRepositoryStub struct {
	repository.AuthRepository
	createErr      error
	createdUser    model.User
	createdSession model.AuthSession
	createdToken   model.AccountToken
}

type rotatingAuthRepository struct {
	repository.AuthRepository
	user     model.User
	sessions map[string]model.AuthSession
}

func (r *rotatingAuthRepository) FindUserByEmail(context.Context, string) (model.User, error) {
	return r.user, nil
}

func (r *rotatingAuthRepository) FindUserByID(context.Context, string) (model.User, error) {
	return r.user, nil
}

func (r *rotatingAuthRepository) CreateSession(_ context.Context, session *model.AuthSession) error {
	r.sessions[session.RefreshTokenHash] = *session
	return nil
}

func (r *rotatingAuthRepository) RotateSession(_ context.Context, tokenHash, nextTokenHash string, now time.Time) (model.AuthSession, error) {
	session, ok := r.sessions[tokenHash]
	if !ok || session.RevokedAt != nil || !session.ExpiresAt.After(now) {
		return model.AuthSession{}, domain.ErrNotFound
	}
	delete(r.sessions, tokenHash)
	session.RefreshTokenHash = nextTokenHash
	session.LastUsedAt = now
	r.sessions[nextTokenHash] = session
	return session, nil
}

func (r *rotatingAuthRepository) RecordAudit(context.Context, *model.AccountAuditLog) error {
	return nil
}

func (r *authRepositoryStub) CreateUserAndSession(_ context.Context, user *model.User, session *model.AuthSession) error {
	r.createdUser = *user
	r.createdSession = *session
	return r.createErr
}

func (r *authRepositoryStub) CreateAccountToken(_ context.Context, token *model.AccountToken) error {
	r.createdToken = *token
	return nil
}

func (r *authRepositoryStub) RecordAudit(context.Context, *model.AccountAuditLog) error { return nil }

func newTestAuthService(repo repository.AuthRepository) *AuthService {
	return NewAuth(
		repo,
		auth.NewJWTManager("test-secret", time.Hour),
		mailer.NoopSender{},
		ratelimit.Noop{},
		nil,
		AuthConfig{
			RefreshTokenTTL:      30 * 24 * time.Hour,
			EmailVerificationTTL: 24 * time.Hour,
			PasswordResetTTL:     30 * time.Minute,
			PublicWebURL:         "http://localhost:5173",
		},
	)
}

func TestAuthServiceMapsRepositoryConflict(t *testing.T) {
	repo := &authRepositoryStub{createErr: domain.ErrAlreadyExists}
	service := newTestAuthService(repo)

	_, err := service.Register(context.Background(), RegisterInput{
		Email:       "demo@example.com",
		Password:    "password123",
		DisplayName: "Demo",
	})

	if !errors.Is(err, ErrEmailAlreadyRegistered) {
		t.Fatalf("Register() error = %v, want ErrEmailAlreadyRegistered", err)
	}
	if repo.createdUser.Email != "demo@example.com" {
		t.Fatalf("repository email = %q, want demo@example.com", repo.createdUser.Email)
	}
	if repo.createdUser.PasswordHash == "" {
		t.Fatal("repository password hash is empty")
	}
}

func TestRegisterStoresOnlyRefreshTokenHash(t *testing.T) {
	repo := &authRepositoryStub{}
	service := newTestAuthService(repo)

	result, err := service.Register(context.Background(), RegisterInput{
		Email: "demo@example.com", Password: "password123", DisplayName: "Demo",
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if result.RefreshToken == "" {
		t.Fatal("raw refresh token is empty")
	}
	if repo.createdSession.RefreshTokenHash == result.RefreshToken {
		t.Fatal("refresh token unexpectedly persisted as plaintext")
	}
	if repo.createdSession.RefreshTokenHash != auth.HashOpaqueToken(result.RefreshToken) {
		t.Fatal("persisted refresh token hash does not match returned token")
	}
}

func TestRefreshTokenRotatesAndCannotBeReused(t *testing.T) {
	passwordHash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	repo := &rotatingAuthRepository{
		user:     model.User{ID: "user-1", Email: "demo@example.com", PasswordHash: passwordHash},
		sessions: make(map[string]model.AuthSession),
	}
	service := newTestAuthService(repo)
	loginResult, err := service.Login(context.Background(), LoginInput{Email: "demo@example.com", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	refreshResult, err := service.Refresh(context.Background(), loginResult.RefreshToken, ClientInfo{})
	if err != nil {
		t.Fatalf("first Refresh() error = %v", err)
	}
	if refreshResult.RefreshToken == loginResult.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if _, err := service.Refresh(context.Background(), loginResult.RefreshToken, ClientInfo{}); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("reused Refresh() error = %v, want ErrInvalidRefreshToken", err)
	}
	if _, err := service.Refresh(context.Background(), refreshResult.RefreshToken, ClientInfo{}); err != nil {
		t.Fatalf("rotated Refresh() error = %v", err)
	}
}

func TestValidEmailRejectsDisplayNamesAndHeaderInjection(t *testing.T) {
	for _, value := range []string{"demo@example.com", "name.surname+tag@example.co.uk"} {
		if !validEmail(value) {
			t.Fatalf("validEmail(%q) = false", value)
		}
	}
	for _, value := range []string{"Demo <demo@example.com>", "demo@example.com\r\nBcc: attacker@example.com", "missing-at.example.com"} {
		if validEmail(value) {
			t.Fatalf("validEmail(%q) = true", value)
		}
	}
}

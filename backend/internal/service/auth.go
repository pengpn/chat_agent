package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/repository"
)

type AuthService struct {
	users repository.UserRepository
	jwt   *auth.JWTManager
}

type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
}

type LoginInput struct {
	Email    string
	Password string
}

type AuthResult struct {
	User        model.User
	AccessToken string
	ExpiresAt   time.Time
}

func NewAuth(users repository.UserRepository, jwt *auth.JWTManager) *AuthService {
	return &AuthService{users: users, jwt: jwt}
}

func (s *AuthService) Register(ctx context.Context, input RegisterInput) (AuthResult, error) {
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if !validEmail(input.Email) {
		return AuthResult{}, ErrInvalidEmail
	}
	if len(input.Password) < 8 {
		return AuthResult{}, ErrPasswordTooShort
	}
	if input.DisplayName == "" {
		input.DisplayName = strings.Split(input.Email, "@")[0]
	}
	if len([]rune(input.DisplayName)) > 80 {
		return AuthResult{}, ErrDisplayNameTooLong
	}

	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		return AuthResult{}, err
	}
	user := model.User{Email: input.Email, PasswordHash: hash, DisplayName: input.DisplayName}
	if err := s.users.Create(ctx, &user); err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return AuthResult{}, ErrEmailAlreadyRegistered
		}
		return AuthResult{}, err
	}
	return s.issue(user)
}

func (s *AuthService) Login(ctx context.Context, input LoginInput) (AuthResult, error) {
	user, err := s.users.FindByEmail(ctx, strings.TrimSpace(strings.ToLower(input.Email)))
	if errors.Is(err, domain.ErrNotFound) {
		return AuthResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return AuthResult{}, err
	}
	if auth.VerifyPassword(user.PasswordHash, input.Password) != nil {
		return AuthResult{}, ErrInvalidCredentials
	}
	return s.issue(user)
}

func (s *AuthService) Me(ctx context.Context, userID string) (model.User, error) {
	user, err := s.users.FindByID(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return model.User{}, ErrUnauthenticated
	}
	return user, err
}

func (s *AuthService) issue(user model.User) (AuthResult, error) {
	token, expiresAt, err := s.jwt.Issue(user.ID, user.Email)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{User: user, AccessToken: token, ExpiresAt: expiresAt}, nil
}

func validEmail(value string) bool {
	return len(value) <= 254 && strings.Count(value, "@") == 1 && !strings.HasPrefix(value, "@") && !strings.HasSuffix(value, "@")
}

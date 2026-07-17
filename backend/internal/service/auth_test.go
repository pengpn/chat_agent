package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/repository"
)

type fakeUserRepository struct {
	createErr error
	created   model.User
}

func (r *fakeUserRepository) Create(_ context.Context, user *model.User) error {
	r.created = *user
	return r.createErr
}

func (r *fakeUserRepository) FindByEmail(context.Context, string) (model.User, error) {
	return model.User{}, domain.ErrNotFound
}

func (r *fakeUserRepository) FindByID(context.Context, string) (model.User, error) {
	return model.User{}, domain.ErrNotFound
}

var _ repository.UserRepository = (*fakeUserRepository)(nil)

func TestAuthServiceMapsRepositoryConflict(t *testing.T) {
	repository := &fakeUserRepository{createErr: domain.ErrAlreadyExists}
	service := NewAuth(repository, auth.NewJWTManager("test-secret", time.Hour))

	_, err := service.Register(context.Background(), RegisterInput{
		Email:       "demo@example.com",
		Password:    "password123",
		DisplayName: "Demo",
	})

	if !errors.Is(err, ErrEmailAlreadyRegistered) {
		t.Fatalf("Register() error = %v, want ErrEmailAlreadyRegistered", err)
	}
	if repository.created.Email != "demo@example.com" {
		t.Fatalf("repository email = %q, want demo@example.com", repository.created.Email)
	}
	if repository.created.PasswordHash == "" {
		t.Fatal("repository password hash is empty")
	}
}

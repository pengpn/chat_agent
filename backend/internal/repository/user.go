package repository

import (
	"context"
	"errors"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// UserRepository defines the user persistence operations used by services.
type UserRepository interface {
	Create(ctx context.Context, user *model.User) error
	FindByEmail(ctx context.Context, email string) (model.User, error)
	FindByID(ctx context.Context, id string) (model.User, error)
	// SetBanned 封禁或解封用户；目标用户不存在时返回 domain.ErrNotFound。
	SetBanned(ctx context.Context, userID string, banned bool, now time.Time) error
	SetRole(ctx context.Context, userID, role string) error
}

// GormUserRepository is the PostgreSQL/GORM implementation of UserRepository.
type GormUserRepository struct{ db *gorm.DB }

func newUserRepository(db *gorm.DB) *GormUserRepository { return &GormUserRepository{db: db} }

func (r *GormUserRepository) Create(ctx context.Context, user *model.User) error {
	err := r.db.WithContext(ctx).Create(user).Error
	if isUniqueViolation(err) {
		return domain.ErrAlreadyExists
	}
	return err
}

func (r *GormUserRepository) FindByEmail(ctx context.Context, email string) (model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Where("email = ?", email).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, domain.ErrNotFound
	}
	return user, err
}

func (r *GormUserRepository) FindByID(ctx context.Context, id string) (model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).First(&user, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, domain.ErrNotFound
	}
	return user, err
}

func (r *GormUserRepository) SetBanned(ctx context.Context, userID string, banned bool, now time.Time) error {
	value := any(nil)
	if banned {
		value = now
	}
	result := r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Update("banned_at", value)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *GormUserRepository) SetRole(ctx context.Context, userID, role string) error {
	result := r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Update("role", role)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

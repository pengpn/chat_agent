package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AccountExport contains only user-owned or user-visible data. Password and
// token hashes are excluded by model JSON tags and are never loaded here.
type AccountExport struct {
	ExportedAt                  time.Time                          `json:"exportedAt"`
	User                        model.User                         `json:"user"`
	Conversations               []model.Conversation               `json:"conversations"`
	Messages                    []model.Message                    `json:"messages"`
	Attachments                 []model.Attachment                 `json:"attachments"`
	KnowledgeBases              []model.KnowledgeBase              `json:"knowledgeBases"`
	KnowledgeDocuments          []model.KnowledgeDocument          `json:"knowledgeDocuments"`
	KnowledgeChunks             []model.KnowledgeChunk             `json:"knowledgeChunks"`
	MessageCitations            []model.MessageCitation            `json:"messageCitations"`
	ConversationSummaryVersions []model.ConversationSummaryVersion `json:"conversationSummaryVersions"`
	AuditLogs                   []model.AccountAuditLog            `json:"accountAuditLogs"`
}

// AuthRepository owns persistence spanning users, device sessions, one-time
// credentials and account lifecycle transactions.
type AuthRepository interface {
	CreateUserAndSession(ctx context.Context, user *model.User, session *model.AuthSession) error
	FindUserByEmail(ctx context.Context, email string) (model.User, error)
	FindUserByID(ctx context.Context, id string) (model.User, error)
	CreateSession(ctx context.Context, session *model.AuthSession) error
	RotateSession(ctx context.Context, tokenHash, nextTokenHash string, now time.Time) (model.AuthSession, error)
	IsSessionActive(ctx context.Context, userID, sessionID string, now time.Time) (bool, error)
	SetUserRole(ctx context.Context, userID, role string) error
	ListSessions(ctx context.Context, userID string) ([]model.AuthSession, error)
	RevokeSession(ctx context.Context, userID, sessionID, reason string, now time.Time) error
	RevokeSessionByTokenHash(ctx context.Context, tokenHash, reason string, now time.Time) (model.AuthSession, error)
	RevokeAllSessions(ctx context.Context, userID, reason string, now time.Time) error
	CreateAccountToken(ctx context.Context, token *model.AccountToken) error
	ConsumeAccountToken(ctx context.Context, tokenHash, purpose string, now time.Time) (model.AccountToken, error)
	VerifyEmailWithToken(ctx context.Context, tokenHash string, now time.Time) (model.User, error)
	ResetPasswordWithToken(ctx context.Context, tokenHash, passwordHash, reason string, now time.Time) (string, error)
	MarkEmailVerified(ctx context.Context, userID string, now time.Time) (model.User, error)
	UpdatePasswordAndRevokeSessions(ctx context.Context, userID, passwordHash, reason string, now time.Time) error
	RecordAudit(ctx context.Context, entry *model.AccountAuditLog) error
	ExportAccount(ctx context.Context, userID string, now time.Time) (AccountExport, error)
	ListObjectKeysByUser(ctx context.Context, userID string) ([]string, error)
	DeleteAccount(ctx context.Context, userID string) error
	CleanupExpiredCredentials(ctx context.Context, now, before time.Time) error
	CleanupAuditLogs(ctx context.Context, before time.Time) error
}

type GormAuthRepository struct{ db *gorm.DB }

func newAuthRepository(db *gorm.DB) *GormAuthRepository { return &GormAuthRepository{db: db} }

func (r *GormAuthRepository) CreateUserAndSession(ctx context.Context, user *model.User, session *model.AuthSession) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		session.UserID = user.ID
		return tx.Create(session).Error
	})
	if isUniqueViolation(err) {
		return domain.ErrAlreadyExists
	}
	return err
}

func (r *GormAuthRepository) FindUserByEmail(ctx context.Context, email string) (model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Where("email = ?", email).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, domain.ErrNotFound
	}
	return user, err
}

func (r *GormAuthRepository) FindUserByID(ctx context.Context, id string) (model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).First(&user, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, domain.ErrNotFound
	}
	return user, err
}

func (r *GormAuthRepository) CreateSession(ctx context.Context, session *model.AuthSession) error {
	return r.db.WithContext(ctx).Create(session).Error
}

func (r *GormAuthRepository) RotateSession(ctx context.Context, tokenHash, nextTokenHash string, now time.Time) (model.AuthSession, error) {
	var session model.AuthSession
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("refresh_token_hash = ?", tokenHash).First(&session).Error; err != nil {
			return err
		}
		if session.RevokedAt != nil || !session.ExpiresAt.After(now) {
			return domain.ErrNotFound
		}
		return tx.Model(&session).Updates(map[string]any{
			"refresh_token_hash": nextTokenHash,
			"last_used_at":       now,
		}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrNotFound
	}
	return session, err
}

func (r *GormAuthRepository) IsSessionActive(ctx context.Context, userID, sessionID string, now time.Time) (bool, error) {
	var count int64
	// 联表检查封禁状态：管理员封禁后，存量会话立即失效，
	// 不需要等待 Access Token 过期。
	err := r.db.WithContext(ctx).Model(&model.AuthSession{}).
		Joins("JOIN users ON users.id = auth_sessions.user_id").
		Where("auth_sessions.id = ? AND auth_sessions.user_id = ? AND auth_sessions.revoked_at IS NULL AND auth_sessions.expires_at > ? AND users.banned_at IS NULL", sessionID, userID, now).
		Count(&count).Error
	return count == 1, err
}

func (r *GormAuthRepository) SetUserRole(ctx context.Context, userID, role string) error {
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Update("role", role).Error
}

func (r *GormAuthRepository) ListSessions(ctx context.Context, userID string) ([]model.AuthSession, error) {
	var sessions []model.AuthSession
	err := r.db.WithContext(ctx).Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now()).
		Order("last_used_at DESC, created_at DESC").Find(&sessions).Error
	return sessions, err
}

func (r *GormAuthRepository) RevokeSession(ctx context.Context, userID, sessionID, reason string, now time.Time) error {
	result := r.db.WithContext(ctx).Model(&model.AuthSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", sessionID, userID).
		Updates(map[string]any{"revoked_at": now, "revoked_reason": reason})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *GormAuthRepository) RevokeSessionByTokenHash(ctx context.Context, tokenHash, reason string, now time.Time) (model.AuthSession, error) {
	var session model.AuthSession
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("refresh_token_hash = ? AND revoked_at IS NULL", tokenHash).First(&session).Error; err != nil {
			return err
		}
		return tx.Model(&session).Updates(map[string]any{"revoked_at": now, "revoked_reason": reason}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrNotFound
	}
	return session, err
}

func (r *GormAuthRepository) RevokeAllSessions(ctx context.Context, userID, reason string, now time.Time) error {
	return r.db.WithContext(ctx).Model(&model.AuthSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Updates(map[string]any{"revoked_at": now, "revoked_reason": reason}).Error
}

func (r *GormAuthRepository) CreateAccountToken(ctx context.Context, token *model.AccountToken) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&model.AccountToken{}).
			Where("user_id = ? AND purpose = ? AND used_at IS NULL", token.UserID, token.Purpose).
			Update("used_at", now).Error; err != nil {
			return err
		}
		return tx.Create(token).Error
	})
}

func (r *GormAuthRepository) ConsumeAccountToken(ctx context.Context, tokenHash, purpose string, now time.Time) (model.AccountToken, error) {
	var token model.AccountToken
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ? AND purpose = ?", tokenHash, purpose).First(&token).Error; err != nil {
			return err
		}
		if token.UsedAt != nil || !token.ExpiresAt.After(now) {
			return domain.ErrNotFound
		}
		return tx.Model(&token).Update("used_at", now).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrNotFound
	}
	return token, err
}

func (r *GormAuthRepository) VerifyEmailWithToken(ctx context.Context, tokenHash string, now time.Time) (model.User, error) {
	var token model.AccountToken
	var user model.User
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ? AND purpose = ?", tokenHash, model.AccountTokenEmailVerification).
			First(&token).Error; err != nil {
			return err
		}
		if token.UsedAt != nil || !token.ExpiresAt.After(now) {
			return domain.ErrNotFound
		}
		if err := tx.Model(&token).Update("used_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.User{}).Where("id = ? AND email_verified_at IS NULL", token.UserID).
			Update("email_verified_at", now).Error; err != nil {
			return err
		}
		return tx.First(&user, "id = ?", token.UserID).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrNotFound
	}
	return user, err
}

func (r *GormAuthRepository) ResetPasswordWithToken(ctx context.Context, tokenHash, passwordHash, reason string, now time.Time) (string, error) {
	var token model.AccountToken
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ? AND purpose = ?", tokenHash, model.AccountTokenPasswordReset).
			First(&token).Error; err != nil {
			return err
		}
		if token.UsedAt != nil || !token.ExpiresAt.After(now) {
			return domain.ErrNotFound
		}
		if err := tx.Model(&token).Update("used_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.User{}).Where("id = ?", token.UserID).Updates(map[string]any{
			"password_hash": passwordHash, "password_changed_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", token.UserID).
			Updates(map[string]any{"revoked_at": now, "revoked_reason": reason}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.ErrNotFound
	}
	return token.UserID, err
}

func (r *GormAuthRepository) MarkEmailVerified(ctx context.Context, userID string, now time.Time) (model.User, error) {
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("id = ? AND email_verified_at IS NULL").
		Update("email_verified_at", now).Error; err != nil {
		return model.User{}, err
	}
	return r.FindUserByID(ctx, userID)
}

func (r *GormAuthRepository) UpdatePasswordAndRevokeSessions(ctx context.Context, userID, passwordHash, reason string, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
			"password_hash": passwordHash, "password_changed_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return domain.ErrNotFound
		}
		return tx.Model(&model.AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", userID).
			Updates(map[string]any{"revoked_at": now, "revoked_reason": reason}).Error
	})
}

func (r *GormAuthRepository) RecordAudit(ctx context.Context, entry *model.AccountAuditLog) error {
	if len(entry.Metadata) == 0 {
		entry.Metadata = json.RawMessage(`{}`)
	}
	return r.db.WithContext(ctx).Create(entry).Error
}

func (r *GormAuthRepository) ExportAccount(ctx context.Context, userID string, now time.Time) (AccountExport, error) {
	user, err := r.FindUserByID(ctx, userID)
	if err != nil {
		return AccountExport{}, err
	}
	result := AccountExport{ExportedAt: now, User: user}
	queries := []struct {
		value any
		query *gorm.DB
	}{
		{&result.Conversations, r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC")},
		{&result.Messages, r.db.WithContext(ctx).Where("conversation_id IN (?)", r.db.Model(&model.Conversation{}).Select("id").Where("user_id = ?", userID)).Order("created_at ASC")},
		{&result.Attachments, r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC")},
		{&result.KnowledgeBases, r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC")},
		{&result.KnowledgeDocuments, r.db.WithContext(ctx).Where("knowledge_base_id IN (?)", r.db.Model(&model.KnowledgeBase{}).Select("id").Where("user_id = ?", userID)).Order("created_at ASC")},
		{&result.KnowledgeChunks, r.db.WithContext(ctx).Where("document_id IN (?)", r.db.Model(&model.KnowledgeDocument{}).Select("id").Where("knowledge_base_id IN (?)", r.db.Model(&model.KnowledgeBase{}).Select("id").Where("user_id = ?", userID))).Order("created_at ASC")},
		{&result.MessageCitations, r.db.WithContext(ctx).Where("message_id IN (?)", r.db.Model(&model.Message{}).Select("id").Where("conversation_id IN (?)", r.db.Model(&model.Conversation{}).Select("id").Where("user_id = ?", userID))).Order("message_id ASC, ordinal ASC")},
		{&result.ConversationSummaryVersions, r.db.WithContext(ctx).Where("conversation_id IN (?)", r.db.Model(&model.Conversation{}).Select("id").Where("user_id = ?", userID)).Order("created_at ASC")},
		{&result.AuditLogs, r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC")},
	}
	for _, query := range queries {
		if err := query.query.Find(query.value).Error; err != nil {
			return AccountExport{}, err
		}
	}
	return result, nil
}

func (r *GormAuthRepository) ListObjectKeysByUser(ctx context.Context, userID string) ([]string, error) {
	var keys []string
	if err := r.db.WithContext(ctx).Model(&model.Attachment{}).Where("user_id = ?", userID).Pluck("object_key", &keys).Error; err != nil {
		return nil, err
	}
	var knowledgeKeys []string
	err := r.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Where("knowledge_base_id IN (?)", r.db.Model(&model.KnowledgeBase{}).Select("id").Where("user_id = ?", userID)).
		Pluck("object_key", &knowledgeKeys).Error
	return append(keys, knowledgeKeys...), err
}

func (r *GormAuthRepository) DeleteAccount(ctx context.Context, userID string) error {
	result := r.db.WithContext(ctx).Delete(&model.User{}, "id = ?", userID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *GormAuthRepository) CleanupAuditLogs(ctx context.Context, before time.Time) error {
	return r.db.WithContext(ctx).Where("created_at < ?", before).Delete(&model.AccountAuditLog{}).Error
}

func (r *GormAuthRepository) CleanupExpiredCredentials(ctx context.Context, now, before time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("expires_at < ? OR (used_at IS NOT NULL AND used_at < ?)", now, before).
			Delete(&model.AccountToken{}).Error; err != nil {
			return err
		}
		return tx.Where("expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)", now, before).
			Delete(&model.AuthSession{}).Error
	})
}

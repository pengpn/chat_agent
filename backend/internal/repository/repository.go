// Package repository contains all GORM queries and persistence operations.
package repository

import (
	"context"

	"github.com/example/agent-platform/backend/internal/database/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const legacyMessageTreeBackfillSQL = `
	UPDATE messages
	SET parent_message_id = reply_to_message_id,
		is_active_child = is_active_response
	WHERE role = 'assistant' AND reply_to_message_id IS NOT NULL;

	UPDATE messages AS user_message
	SET parent_message_id = (
		SELECT candidate.id
		FROM messages AS candidate
		WHERE candidate.conversation_id = user_message.conversation_id
			AND candidate.role = 'assistant'
			AND candidate.created_at < user_message.created_at
		ORDER BY candidate.created_at DESC
		LIMIT 1
	),
		is_active_child = TRUE
	WHERE user_message.role = 'user';

	UPDATE messages
	SET is_active_child = TRUE
	WHERE role = 'user' AND parent_message_id IS NULL;
`

const messageTreeMigrationVersion = "20260807_message_tree_v1"

// Repositories groups model-specific repositories and provides transaction
// boundaries for services that coordinate multiple persistence operations.
type Repositories struct {
	db            *gorm.DB
	Auth          AuthRepository
	Users         UserRepository
	Conversations ConversationRepository
	Messages      MessageRepository
	Attachments   AttachmentRepository
	Quotas        QuotaRepository
	Knowledge     KnowledgeRepository
}

// ChatRepository groups the persistence operations required for a conversation
// turn, whether it uses Chat or ChatStream.
type ChatRepository interface {
	FindByIDForUser(ctx context.Context, userID, id string) (model.Conversation, error)
	ListByConversationForUser(ctx context.Context, userID, conversationID string) ([]model.Message, error)
	FindMessageByIDForUser(ctx context.Context, userID, conversationID, messageID string) (model.Message, error)
	CreateTurn(ctx context.Context, userMessage, assistantMessage *model.Message) error
	CreateRegeneration(ctx context.Context, userID, targetMessageID, replyToMessageID string, assistant *model.Message) error
	CreateEditedTurn(ctx context.Context, userID, targetMessageID string, userMessage, assistantMessage *model.Message) error
	Finalize(ctx context.Context, id string, completion model.MessageCompletion) (model.Message, error)
	SaveConversationSummaryVersion(ctx context.Context, userID string, version *model.ConversationSummaryVersion, activate bool) error
}

func New(db *gorm.DB) *Repositories {
	return &Repositories{
		db:            db,
		Auth:          newAuthRepository(db),
		Users:         newUserRepository(db),
		Conversations: newConversationRepository(db),
		Messages:      newMessageRepository(db),
		Attachments:   newAttachmentRepository(db),
		Quotas:        newQuotaRepository(db),
		Knowledge:     newKnowledgeRepository(db),
	}
}

func (r *Repositories) transaction(ctx context.Context, fn func(tx *Repositories) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(New(tx))
	})
}

// CreateTurn stores the user and assistant messages together, then advances
// the conversation timestamp. It is kept atomic so service code does not need
// to understand GORM transactions.
func (r *Repositories) CreateTurn(ctx context.Context, userMessage, assistantMessage *model.Message) error {
	return r.CreateTurnWithAttachments(ctx, "", nil, userMessage, assistantMessage)
}

// CreateTurnWithAttachments binds ready, unclaimed attachments in the same
// transaction that creates the user and assistant messages.
func (r *Repositories) CreateTurnWithAttachments(ctx context.Context, userID string, attachmentIDs []string, userMessage, assistantMessage *model.Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conversation model.Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userMessage.ConversationID).First(&conversation).Error; err != nil {
			return err
		}
		if userMessage.ParentMessageID != nil {
			var parent model.Message
			if err := tx.Where("id = ? AND conversation_id = ?", *userMessage.ParentMessageID, userMessage.ConversationID).
				First(&parent).Error; err != nil {
				return err
			}
			if err := activateMessagePath(tx, parent); err != nil {
				return err
			}
		}
		var siblings int64
		if err := siblingQuery(tx, userMessage.ConversationID, "user", userMessage.ParentMessageID).Count(&siblings).Error; err != nil {
			return err
		}
		if err := siblingQuery(tx, userMessage.ConversationID, "user", userMessage.ParentMessageID).Update("is_active_child", false).Error; err != nil {
			return err
		}
		userMessage.IsActiveChild = true
		if err := tx.Create(userMessage).Error; err != nil {
			return err
		}
		if len(attachmentIDs) > 0 {
			if err := r.Attachments.ClaimForMessage(ctx, tx, userID, userMessage.ConversationID, userMessage.ID, attachmentIDs); err != nil {
				return err
			}
		}
		assistantMessage.ParentMessageID = &userMessage.ID
		assistantMessage.ReplyToMessageID = &userMessage.ID
		assistantMessage.ResponseVersion = 1
		assistantMessage.IsActiveResponse = true
		assistantMessage.IsActiveChild = true
		if err := tx.Create(assistantMessage).Error; err != nil {
			return err
		}
		if siblings > 0 {
			return invalidateConversationSummary(tx, userMessage.ConversationID, true)
		}
		return tx.Model(&model.Conversation{}).Where("id = ?", userMessage.ConversationID).
			Update("updated_at", gorm.Expr("CURRENT_TIMESTAMP")).Error
	})
}

func (r *Repositories) FindMessageByIDForUser(ctx context.Context, userID, conversationID, messageID string) (model.Message, error) {
	return r.Messages.FindByIDForUser(ctx, userID, conversationID, messageID)
}

func (r *Repositories) CreateRegeneration(ctx context.Context, userID, targetMessageID, replyToMessageID string, assistant *model.Message) error {
	return r.Messages.CreateRegeneration(ctx, userID, targetMessageID, replyToMessageID, assistant)
}

func (r *Repositories) CreateEditedTurn(ctx context.Context, userID, targetMessageID string, userMessage, assistantMessage *model.Message) error {
	return r.Messages.CreateEditedTurn(ctx, userID, targetMessageID, userMessage, assistantMessage)
}

func (r *Repositories) FindByIDForUser(ctx context.Context, userID, id string) (model.Conversation, error) {
	return r.Conversations.FindByIDForUser(ctx, userID, id)
}

func (r *Repositories) ListByConversationForUser(ctx context.Context, userID, conversationID string) ([]model.Message, error) {
	return r.Messages.ListByConversationForUser(ctx, userID, conversationID)
}

func (r *Repositories) Finalize(ctx context.Context, id string, completion model.MessageCompletion) (model.Message, error) {
	return r.Messages.Finalize(ctx, id, completion)
}

func (r *Repositories) SaveConversationSummaryVersion(ctx context.Context, userID string, version *model.ConversationSummaryVersion, activate bool) error {
	return r.Conversations.SaveSummaryVersion(ctx, userID, version, activate)
}

func Migrate(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS pgcrypto").Error; err != nil {
		return err
	}
	if err := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
		return err
	}
	if err := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return err
	}
	if err := db.WithContext(ctx).AutoMigrate(
		&model.User{},
		&model.AuthSession{},
		&model.AccountToken{},
		&model.AccountAuditLog{},
		&model.Conversation{},
		&model.Message{},
		&model.Attachment{},
		&model.AttachmentChunk{},
		&model.ConversationSummaryVersion{},
		&model.UserQuota{},
		&model.KnowledgeBase{},
		&model.KnowledgeDocument{},
		&model.KnowledgeChunk{},
		&model.MessageCitation{},
	); err != nil {
		return err
	}
	if err := ensureKnowledgeForeignKeys(ctx, db); err != nil {
		return err
	}
	if err := migrateMessageTree(ctx, db); err != nil {
		return err
	}
	if err := ensurePaginationAndSearchIndexes(ctx, db); err != nil {
		return err
	}
	// Titles created by older versions came either from the user or from the
	// former frontend truncation. Treat them as manual so background generation
	// can never overwrite them after this upgrade.
	if err := db.WithContext(ctx).Exec(`
		UPDATE conversations
		SET title_status = 'manual'
		WHERE title_status = 'default' AND title <> 'New conversation'
	`).Error; err != nil {
		return err
	}
	// GORM does not update an existing named CHECK constraint when its tag
	// changes, so replace it explicitly when adding a new message state.
	return db.WithContext(ctx).Exec(`
		ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_status_check;
		ALTER TABLE messages ADD CONSTRAINT messages_status_check
		CHECK (status IN ('streaming', 'complete', 'stopped', 'interrupted', 'error'));
		ALTER TABLE conversations DROP CONSTRAINT IF EXISTS conversations_title_status_check;
		ALTER TABLE conversations ADD CONSTRAINT conversations_title_status_check
		CHECK (title_status IN ('default', 'generating', 'generated', 'fallback', 'manual'))
	`).Error
}

// ensureKnowledgeForeignKeys makes deletion semantics deterministic for both
// new and already migrated databases. GORM derives has-many constraints from
// the parent association and AutoMigrate does not alter an existing constraint
// when only its ON DELETE action changes.
func ensureKnowledgeForeignKeys(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "20260911_knowledge_foreign_keys_v1").Error; err != nil {
			return err
		}
		var cascadeCount int64
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM pg_constraint
			WHERE conname IN ('fk_knowledge_bases_documents', 'fk_messages_citations')
				AND contype = 'f'
				AND confdeltype = 'c'
		`).Scan(&cascadeCount).Error; err != nil {
			return err
		}
		if cascadeCount == 2 {
			return nil
		}
		return tx.Exec(`
		ALTER TABLE knowledge_documents
		DROP CONSTRAINT IF EXISTS fk_knowledge_bases_documents;
		ALTER TABLE knowledge_documents
		ADD CONSTRAINT fk_knowledge_bases_documents
		FOREIGN KEY (knowledge_base_id) REFERENCES knowledge_bases(id) ON DELETE CASCADE;

		ALTER TABLE message_citations
		DROP CONSTRAINT IF EXISTS fk_messages_citations;
		ALTER TABLE message_citations
		ADD CONSTRAINT fk_messages_citations
		FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
		`).Error
	})
}

func ensurePaginationAndSearchIndexes(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Exec(`
		CREATE INDEX IF NOT EXISTS conversations_user_updated_cursor_idx
		ON conversations (user_id, updated_at DESC, id DESC);
		CREATE INDEX IF NOT EXISTS messages_conversation_created_cursor_idx
		ON messages (conversation_id, created_at DESC, id DESC);
		CREATE INDEX IF NOT EXISTS conversations_title_trgm_idx
		ON conversations USING GIN (title gin_trgm_ops);
		CREATE INDEX IF NOT EXISTS messages_content_trgm_idx
		ON messages USING GIN (content gin_trgm_ops)
	`).Error
}

// migrateMessageTree uses an explicit marker instead of checking whether the
// new column exists. AutoMigrate may add the column successfully and then the
// data backfill may fail; without a marker, the next startup would incorrectly
// treat that partially migrated database as complete.
func migrateMessageTree(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec(`
		CREATE TABLE IF NOT EXISTS app_schema_migrations (
			version VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`).Error; err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize startup migration across application instances.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", messageTreeMigrationVersion).Error; err != nil {
			return err
		}
		var applied int64
		if err := tx.Raw("SELECT COUNT(*) FROM app_schema_migrations WHERE version = ?", messageTreeMigrationVersion).
			Scan(&applied).Error; err != nil {
			return err
		}
		if applied > 0 {
			return nil
		}
		// One-time upgrade for rows created before the conversation tree existed.
		// Assistant messages already point at their user through reply_to_message_id.
		// Each legacy user is attached to the latest assistant that existed before it.
		if err := tx.Exec(legacyMessageTreeBackfillSQL).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO app_schema_migrations (version) VALUES (?)", messageTreeMigrationVersion).Error
	})
}

package repository

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresConcurrentEditsAndConversationCascade(t *testing.T) {
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
	repositories := New(db)
	user := model.User{
		Email: fmt.Sprintf("branch-test-%d@example.com", time.Now().UnixNano()), PasswordHash: "test-only", DisplayName: "Branch Test",
	}
	if err := repositories.Users.Create(ctx, &user); err != nil {
		t.Fatal(err)
	}
	defer db.WithContext(ctx).Delete(&user)
	conversation := model.Conversation{UserID: user.ID, Title: "branch integration test"}
	if err := repositories.Conversations.Create(ctx, &conversation); err != nil {
		t.Fatal(err)
	}
	rootUser := model.Message{ConversationID: conversation.ID, Role: "user", Content: "original", Status: "complete"}
	rootAssistant := model.Message{ConversationID: conversation.ID, Role: "assistant", Provider: "test", Model: "test", Status: "complete"}
	if err := repositories.CreateTurn(ctx, &rootUser, &rootAssistant); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			key := fmt.Sprintf("branch-edit-%d-%d", time.Now().UnixNano(), index)
			editedUser := model.Message{ConversationID: conversation.ID, Role: "user", Content: fmt.Sprintf("edit %d", index), Status: "complete"}
			editedAssistant := model.Message{ConversationID: conversation.ID, Role: "assistant", Provider: "test", Model: "test", Status: "streaming", GenerationKey: &key}
			errorsFound <- repositories.CreateEditedTurn(ctx, user.ID, rootUser.ID, &editedUser, &editedAssistant)
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}

	var editedCount, activeRootCount int64
	if err := db.Model(&model.Message{}).
		Where("conversation_id = ? AND role = 'user' AND edited_from IS NOT NULL", conversation.ID).Count(&editedCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Message{}).
		Where("conversation_id = ? AND role = 'user' AND parent_message_id IS NULL AND is_active_child = TRUE", conversation.ID).
		Count(&activeRootCount).Error; err != nil {
		t.Fatal(err)
	}
	if editedCount != 2 || activeRootCount != 1 {
		t.Fatalf("edited branches=%d active roots=%d", editedCount, activeRootCount)
	}

	if err := repositories.Conversations.Delete(ctx, user.ID, conversation.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := db.Model(&model.Message{}).Where("conversation_id = ?", conversation.ID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("messages remaining after conversation delete: %d", remaining)
	}
}

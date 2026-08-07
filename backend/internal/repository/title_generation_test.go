package repository

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresTitleClaimIsExclusiveAndManualRenameWins(t *testing.T) {
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
	user := model.User{Email: fmt.Sprintf("title-test-%d@example.com", time.Now().UnixNano()), PasswordHash: "test-only", DisplayName: "Title Test"}
	if err := repositories.Users.Create(ctx, &user); err != nil {
		t.Fatal(err)
	}
	defer db.WithContext(ctx).Delete(&user)
	conversation := model.Conversation{UserID: user.ID, Title: "New conversation", TitleStatus: "default"}
	if err := repositories.Conversations.Create(ctx, &conversation); err != nil {
		t.Fatal(err)
	}
	question := model.Message{ConversationID: conversation.ID, Role: "user", Content: "如何学习 Agent？", Status: "complete"}
	answer := model.Message{ConversationID: conversation.ID, Role: "assistant", Content: "从接口开始。", Provider: "test", Model: "test", Status: "complete"}
	if err := repositories.CreateTurn(ctx, &question, &answer); err != nil {
		t.Fatal(err)
	}

	var claimed atomic.Int64
	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, ok, claimErr := repositories.Conversations.ClaimTitleGeneration(ctx, user.ID, conversation.ID, time.Now().Add(-time.Minute))
			if ok {
				claimed.Add(1)
			}
			errorsFound <- claimErr
		}()
	}
	wait.Wait()
	close(errorsFound)
	for claimErr := range errorsFound {
		if claimErr != nil {
			t.Fatal(claimErr)
		}
	}
	if claimed.Load() != 1 {
		t.Fatalf("successful claims = %d, want 1", claimed.Load())
	}

	manual, err := repositories.Conversations.UpdateTitle(ctx, user.ID, conversation.ID, "我的标题")
	if err != nil {
		t.Fatal(err)
	}
	if manual.TitleStatus != "manual" {
		t.Fatalf("manual conversation = %#v", manual)
	}
	completed, updated, err := repositories.Conversations.CompleteTitleGeneration(ctx, user.ID, conversation.ID, "模型标题", "generated", TitleGenerationUsage{})
	if err != nil {
		t.Fatal(err)
	}
	if updated || completed.Title != "我的标题" || completed.TitleStatus != "manual" {
		t.Fatalf("completed=%#v updated=%t", completed, updated)
	}
}

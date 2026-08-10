package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/pagination"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestPostgresPaginationAndSearchWithTenThousandMessages is an opt-in
// integration/performance check. It uses isolated rows and deletes both test
// users at the end, so it can run against a dedicated local test database.
func TestPostgresPaginationAndSearchWithTenThousandMessages(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{CreateBatchSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repositories := New(db)
	suffix := time.Now().UnixNano()
	owner := model.User{Email: fmt.Sprintf("pagination-owner-%d@example.com", suffix), PasswordHash: "test-only", DisplayName: "Pagination Owner"}
	other := model.User{Email: fmt.Sprintf("pagination-other-%d@example.com", suffix), PasswordHash: "test-only", DisplayName: "Pagination Other"}
	for _, user := range []*model.User{&owner, &other} {
		if err := repositories.Users.Create(ctx, user); err != nil {
			t.Fatal(err)
		}
		defer db.WithContext(ctx).Delete(user)
	}
	conversation := model.Conversation{UserID: owner.ID, Title: "ten thousand message benchmark", TitleStatus: "manual"}
	otherConversation := model.Conversation{UserID: other.ID, Title: "private conversation marker", TitleStatus: "manual"}
	for _, item := range []*model.Conversation{&conversation, &otherConversation} {
		if err := repositories.Conversations.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	messages := make([]model.Message, 10_000)
	base := time.Now().Add(-10_000 * time.Second)
	for index := range messages {
		content := fmt.Sprintf("ordinary benchmark message %05d", index)
		if index == 9_876 {
			content = "owner-search-marker-9876"
		}
		messages[index] = model.Message{
			ConversationID: conversation.ID, Role: "user", Content: content,
			Status: "complete", CreatedAt: base.Add(time.Duration(index) * time.Second),
		}
	}
	if err := db.WithContext(ctx).CreateInBatches(&messages, 500).Error; err != nil {
		t.Fatal(err)
	}
	privateMessage := model.Message{ConversationID: otherConversation.ID, Role: "user", Content: "private-other-user-marker", Status: "complete"}
	if err := db.WithContext(ctx).Create(&privateMessage).Error; err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	first, err := repositories.Messages.ListPageByConversationForUser(ctx, owner.ID, conversation.ID, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("first message page took %s", elapsed)
	}
	if len(first) != 51 {
		t.Fatalf("first page rows = %d, want 51", len(first))
	}
	unauthorized, err := repositories.Messages.ListPageByConversationForUser(ctx, other.ID, conversation.ID, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(unauthorized) != 0 {
		t.Fatalf("message page leaked another user's messages: %d rows", len(unauthorized))
	}
	cursor := &pagination.Cursor{Time: first[49].CreatedAt, ID: first[49].ID}
	second, err := repositories.Messages.ListPageByConversationForUser(ctx, owner.ID, conversation.ID, cursor, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, 50)
	for _, message := range first[:50] {
		seen[message.ID] = struct{}{}
	}
	for _, message := range second {
		if _, duplicated := seen[message.ID]; duplicated {
			t.Fatalf("message %s was duplicated across cursor pages", message.ID)
		}
	}

	matched, err := repositories.Conversations.ListPageByUserID(ctx, owner.ID, ConversationPageQuery{Limit: 10, Search: "owner-search-marker-9876"})
	if err != nil || len(matched) != 1 || matched[0].ID != conversation.ID {
		t.Fatalf("owner search = %#v, error = %v", matched, err)
	}
	leaked, err := repositories.Conversations.ListPageByUserID(ctx, owner.ID, ConversationPageQuery{Limit: 10, Search: "private-other-user-marker"})
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked) != 0 {
		t.Fatalf("search leaked another user's conversation: %#v", leaked)
	}
}

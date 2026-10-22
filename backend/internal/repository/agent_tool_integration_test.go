package repository

import (
	"context"
	"encoding/json"
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

func TestPostgresAgentToolsPersistenceApprovalAndCascade(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未配置 TEST_DATABASE_URL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	u := model.User{Email: fmt.Sprintf("agent-tools-%d@example.com", time.Now().UnixNano()), PasswordHash: "test-only", DisplayName: "工具集成测试"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&u)
	c := model.Conversation{UserID: u.ID, Title: "工具测试"}
	if err := r.Conversations.Create(ctx, &c); err != nil {
		t.Fatal(err)
	}
	p := model.AgentPlan{UserID: u.ID, ConversationID: c.ID, Revision: 1, Steps: json.RawMessage(`[{"title":"test","status":"pending"}]`)}
	for range 2 {
		if err := r.AgentTools.SavePlan(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := r.AgentTools.GetPlan(ctx, u.ID, c.ID)
	if err != nil || plan.Revision != 2 {
		t.Fatalf("计划修订: %+v %v", plan, err)
	}
	item := model.ToolInteraction{UserID: u.ID, ConversationID: c.ID, OperationID: "run", Kind: "approval", Status: "pending", ApprovalKey: "sha-key", Request: json.RawMessage(`{"tool":"shell_exec"}`), ExpiresAt: time.Now().Add(time.Minute)}
	if err := r.AgentTools.CreateInteraction(ctx, &item); err != nil {
		t.Fatal(err)
	}
	if err := r.AgentTools.Respond(ctx, u.ID, c.ID, item.ID, json.RawMessage(`{"approved":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.AgentTools.Respond(ctx, u.ID, c.ID, item.ID, json.RawMessage(`{"approved":true}`)); err != ErrInteractionClosed {
		t.Fatalf("重复响应: %v", err)
	}
	for _, test := range []struct{ u, c, run, key string }{{u.ID, c.ID, "other", "sha-key"}, {u.ID, c.ID, "run", "changed"}, {u.ID, "00000000-0000-0000-0000-000000000000", "run", "sha-key"}} {
		if ok, err := r.AgentTools.ConsumeApproval(ctx, test.u, test.c, test.run, test.key); err != nil || ok {
			t.Fatalf("审批越界: %t %v", ok, err)
		}
	}
	var consumed atomic.Int64
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			ok, err := r.AgentTools.ConsumeApproval(ctx, u.ID, c.ID, "run", "sha-key")
			if err != nil {
				t.Error(err)
			}
			if ok {
				consumed.Add(1)
			}
		}()
	}
	group.Wait()
	if consumed.Load() != 1 {
		t.Fatalf("并发重复消费: %d", consumed.Load())
	}
	for _, kind := range []string{"question", "approval"} {
		i := model.ToolInteraction{UserID: u.ID, ConversationID: c.ID, OperationID: "run", Kind: kind, Status: "responded", ApprovalKey: "rejected", Request: json.RawMessage(`{}`), Response: json.RawMessage(`{"approved":false}`), ExpiresAt: time.Now().Add(time.Minute)}
		if err := r.AgentTools.CreateInteraction(ctx, &i); err != nil {
			t.Fatal(err)
		}
		if ok, err := r.AgentTools.ConsumeApproval(ctx, u.ID, c.ID, "run", "rejected"); err != nil || ok {
			t.Fatalf("拒绝也能授权: %t %v", ok, err)
		}
	}
	a := model.AgentArtifact{UserID: u.ID, ConversationID: c.ID, Title: "报告", MediaType: "text/plain", Content: "你好", Size: 6, SHA256: "test"}
	if err := r.AgentTools.SaveArtifact(ctx, &a); err != nil {
		t.Fatal(err)
	}
	list, err := r.AgentTools.ListArtifacts(ctx, u.ID, c.ID)
	if err != nil || len(list) != 1 || list[0].Content != "" {
		t.Fatalf("列表不应加载正文: %+v %v", list, err)
	}
	full, err := r.AgentTools.GetArtifact(ctx, u.ID, c.ID, a.ID)
	if err != nil || full.Content != "你好" {
		t.Fatalf("内容没有持久化: %+v %v", full, err)
	}
	if err := r.Conversations.Delete(ctx, u.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{&model.AgentPlan{}, &model.ToolInteraction{}, &model.AgentArtifact{}} {
		var count int64
		if err := db.Model(value).Where("conversation_id = ?", c.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("级联清理失败: %T %d %v", value, count, err)
		}
	}
}

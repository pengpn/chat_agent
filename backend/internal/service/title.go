package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
)

const titleSystemPrompt = `Generate a concise conversation title from the user's first question and the assistant's answer. Return only the title, with no quotes, prefix, Markdown, or trailing punctuation. Preserve the user's language and intent.`

var (
	markdownLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\([^\)]+\)`)
	titlePrefixPattern  = regexp.MustCompile(`(?i)^(title|conversation title|标题|对话标题)\s*[:：-]\s*`)
	spacePattern        = regexp.MustCompile(`\s+`)
)

type TitleConfig struct {
	Provider        string
	Model           string
	MaxOutputTokens int64
	MaxLength       int
	Timeout         time.Duration
}

type TitleService struct {
	repository repository.ConversationRepository
	gateway    llmgateway.Gateway
	events     UserEventPublisher
	config     TitleConfig
}

type UserEventPublisher interface {
	Publish(userID, name string, payload map[string]any)
}

func NewTitle(repository repository.ConversationRepository, gateway llmgateway.Gateway, events UserEventPublisher, config TitleConfig) *TitleService {
	if config.MaxOutputTokens <= 0 {
		config.MaxOutputTokens = 64
	}
	if config.MaxLength <= 0 {
		config.MaxLength = 40
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Second
	}
	return &TitleService{repository: repository, gateway: gateway, events: events, config: config}
}

// Schedule detaches title generation from the completed HTTP stream while
// retaining request-scoped log fields. ClaimTitleGeneration is the durable
// concurrency guard, so duplicate completion notifications remain harmless.
func (s *TitleService) Schedule(parent context.Context, userID, conversationID string) {
	if s == nil {
		return
	}
	base := context.WithoutCancel(parent)
	go func() {
		ctx, cancel := context.WithTimeout(base, s.config.Timeout)
		defer cancel()
		if _, err := s.Generate(ctx, userID, conversationID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			logger.Error(ctx, "conversation title generation failed", logger.String("conversation_id", conversationID), logger.Err(err))
		}
	}()
}

// Generate performs one claim/call/finalize cycle and is synchronous so tests
// and future job-queue workers can invoke the same business operation.
func (s *TitleService) Generate(ctx context.Context, userID, conversationID string) (model.Conversation, error) {
	staleBefore := time.Now().Add(-maxDuration(2*s.config.Timeout, time.Minute))
	claim, claimed, err := s.repository.ClaimTitleGeneration(ctx, userID, conversationID, staleBefore)
	if err != nil || !claimed {
		return claim.Conversation, err
	}
	providerID, modelName := claim.Answer.Provider, claim.Answer.Model
	if strings.TrimSpace(s.config.Provider) != "" && strings.TrimSpace(s.config.Model) != "" {
		providerID, modelName = s.config.Provider, s.config.Model
	}
	request := provider.ChatRequest{
		Model:           modelName,
		MaxOutputTokens: s.config.MaxOutputTokens,
		Messages: []provider.Message{
			{Role: "system", Content: titleSystemPrompt},
			{Role: "user", Content: titleSource(claim.UserMessage.Content, claim.Answer.Content)},
		},
	}
	response, callErr := s.gateway.Chat(ctx, llmgateway.Request{
		OperationID: fmt.Sprintf("title:%s:%s", userID, conversationID),
		Purpose:     "conversation_title",
		UserID:      userID,
		Provider:    providerID,
		Model:       modelName,
		ChatRequest: request,
	})
	usage := repository.TitleGenerationUsage{Provider: providerID, Model: modelName}
	if response.Usage != nil {
		usage.InputTokens = response.Usage.InputTokens
		usage.OutputTokens = response.Usage.OutputTokens
		usage.TotalTokens = response.Usage.TotalTokens
		usage.UsageReported = true
		usage.CostNanoUSD, usage.CostKnown = s.gateway.CalculateCost(providerID, modelName, *response.Usage)
	}
	status := "generated"
	title := sanitizeConversationTitle(response.Content, s.config.MaxLength)
	if callErr != nil || title == "" {
		status = "fallback"
		title = sanitizeConversationTitle(claim.UserMessage.Content, s.config.MaxLength)
		if title == "" {
			title = "New conversation"
		}
	}
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelPersist()
	conversation, updated, persistErr := s.repository.CompleteTitleGeneration(persistCtx, userID, conversationID, title, status, usage)
	if persistErr != nil {
		return model.Conversation{}, persistErr
	}
	if !updated {
		logger.Info(ctx, "conversation title result ignored after title changed",
			logger.String("conversation_id", conversationID), logger.String("title_status", status))
		return conversation, nil
	}
	fields := []logger.Field{
		logger.String("conversation_id", conversationID), logger.String("title_status", status),
		logger.String("provider", providerID), logger.String("model", modelName),
		logger.Int64("input_tokens", usage.InputTokens), logger.Int64("output_tokens", usage.OutputTokens),
		logger.Int64("total_tokens", usage.TotalTokens), logger.Bool("usage_reported", usage.UsageReported),
		logger.Int64("cost_nano_usd", usage.CostNanoUSD), logger.Bool("cost_known", usage.CostKnown),
	}
	if callErr != nil {
		fields = append(fields, logger.Err(callErr))
		logger.Warn(ctx, "conversation title fallback saved", fields...)
	} else {
		logger.Info(ctx, "conversation title generated", fields...)
	}
	if s.events != nil {
		s.events.Publish(userID, "conversation.title.updated", map[string]any{"conversation": conversation})
	}
	return conversation, nil
}

func titleSource(question, answer string) string {
	const maxSourceRunes = 4_000
	question = truncate(strings.TrimSpace(question), maxSourceRunes/2)
	answer = truncate(strings.TrimSpace(answer), maxSourceRunes/2)
	return "User question:\n" + question + "\n\nAssistant answer:\n" + answer
}

func sanitizeConversationTitle(value string, maxLength int) string {
	value = markdownLinkPattern.ReplaceAllString(value, "$1")
	value = strings.NewReplacer("\r", " ", "\n", " ", "`", "", "*", "", "_", "", "~", "", "#", "").Replace(value)
	value = spacePattern.ReplaceAllString(strings.TrimSpace(value), " ")
	value = titlePrefixPattern.ReplaceAllString(value, "")
	value = strings.Trim(value, " \t\"'“”‘’《》<>:：;；,.，。!?！？-—")
	return truncate(value, maxLength)
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/alerting"
	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/moderation"
	"github.com/example/agent-platform/backend/internal/outputformat"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/tool"
)

type ChatService struct {
	repository    repository.ChatRepository
	gateway       llmgateway.Gateway
	tools         *tool.Registry
	outputFormats *outputformat.Registry
	maxToolCalls  int
	thinking      *provider.ThinkingConfig
	systemPrompt  string
	stream        StreamConfig
	context       ContextConfig
	attachments   *AttachmentService
	retrieval     KnowledgeRetriever
	// 准入与安全依赖，由 ConfigureSafety 注入，保持 nil 时行为不变。
	quotas           *QuotaService
	moderator        moderation.Moderator
	alerts           alerting.Alerter
	costAlertNanoUSD int64
	maxMessageChars  int
}

type StreamConfig struct {
	TTFTTimeout     time.Duration
	TotalTimeout    time.Duration
	FinalizeTimeout time.Duration
}

type ContextConfig struct {
	SummaryEnabled     bool
	SummaryProvider    string
	SummaryModel       string
	SummaryMaxTokens   int64
	KeepRecentMessages int
	SummaryMaxCalls    int
	QualityEnabled     bool
	QualityThreshold   int
	QualityMaxTokens   int64
}

type ChatInput struct {
	Content          string
	Provider         string
	Model            string
	Temperature      *float64
	OutputFormat     string
	AttachmentIDs    []string
	KnowledgeBaseIDs []string
	// IdempotencyKey comes from the HTTP Idempotency-Key header. It scopes one
	// complete business message rather than individual model retries.
	IdempotencyKey string
}

type ChatSession struct {
	UserMessage              model.Message
	AssistantMessage         model.Message
	OperationID              string
	UserID                   string
	IsRegeneration           bool
	IsEdit                   bool
	ContextIsExplicitPath    bool
	RegeneratedFromMessageID string
	request                  provider.ChatRequest
	preparationUsage         provider.TokenUsage
	preparationUsageReported bool
	preparationCostNanoUSD   int64
	preparationCostKnown     bool
	preparationCostTracked   bool
	groundingMatches         []KnowledgeRetrievalMatch
	// quotaReservedTokens 是本次请求预扣的估算 Token；
	// 请求结束时必须结算或补偿，0 表示未预扣。
	quotaReservedTokens int64
}

type RegenerateInput struct {
	Provider       string
	Model          string
	Temperature    *float64
	OutputFormat   string
	IdempotencyKey string
}

func NewChat(repository repository.ChatRepository, gateway llmgateway.Gateway, tools *tool.Registry, outputFormats *outputformat.Registry, maxToolCalls int, thinking *provider.ThinkingConfig, systemPrompt string, stream StreamConfig, contextConfig ContextConfig, attachmentServices ...*AttachmentService) *ChatService {
	if outputFormats == nil {
		outputFormats = outputformat.NewDefaultRegistry()
	}
	if contextConfig.SummaryMaxTokens <= 0 {
		contextConfig.SummaryMaxTokens = 1024
	}
	if contextConfig.KeepRecentMessages <= 0 {
		contextConfig.KeepRecentMessages = 8
	}
	if contextConfig.SummaryMaxCalls <= 0 {
		contextConfig.SummaryMaxCalls = 2
	}
	if contextConfig.QualityThreshold <= 0 {
		contextConfig.QualityThreshold = 70
	}
	if contextConfig.QualityMaxTokens <= 0 {
		contextConfig.QualityMaxTokens = 256
	}
	var attachments *AttachmentService
	if len(attachmentServices) > 0 {
		attachments = attachmentServices[0]
	}
	return &ChatService{repository: repository, gateway: gateway, tools: tools, outputFormats: outputFormats, maxToolCalls: maxToolCalls, thinking: thinking, systemPrompt: systemPrompt, stream: stream, context: contextConfig, attachments: attachments}
}

// SafetyDependencies 聚合额度、内容审核与告警依赖。
// 任一字段为零值时保持原有行为，方便测试逐步注入。
type SafetyDependencies struct {
	Quotas           *QuotaService
	Moderator        moderation.Moderator
	Alerts           alerting.Alerter
	CostAlertNanoUSD int64
	MaxMessageChars  int
}

func (s *ChatService) ConfigureSafety(deps SafetyDependencies) {
	if deps.Quotas != nil {
		s.quotas = deps.Quotas
	}
	if deps.Moderator != nil {
		s.moderator = deps.Moderator
	}
	if deps.Alerts != nil {
		s.alerts = deps.Alerts
	}
	if deps.CostAlertNanoUSD > 0 {
		s.costAlertNanoUSD = deps.CostAlertNanoUSD
	}
	if deps.MaxMessageChars > 0 {
		s.maxMessageChars = deps.MaxMessageChars
	}
}

// ConfigureKnowledgeRetrieval 为携带知识库 ID 的对话请求开启服务端 RAG。
// 检索到的原文只由服务端注入，客户端传来的原始片段一律不接受。
func (s *ChatService) ConfigureKnowledgeRetrieval(retrieval KnowledgeRetriever) {
	s.retrieval = retrieval
}

func (s *ChatService) messageLimit() int {
	if s.maxMessageChars > 0 {
		return s.maxMessageChars
	}
	return 100_000
}

// Prepare 校验并落库一轮新的用户提问，组装可供 Stream / Chat 使用的会话。
// 负责输入校验、附件绑定、分支父消息选择、上下文准备和配额预扣，但不调用模型。
func (s *ChatService) Prepare(ctx context.Context, userID, conversationID string, input ChatInput) (*ChatSession, error) {
	input.Content = strings.TrimSpace(input.Content)
	if (input.Content == "" && len(input.AttachmentIDs) == 0) || len([]rune(input.Content)) > s.messageLimit() {
		return nil, ErrInvalidMessageLength
	}
	if blocked := s.checkInputModeration(ctx, userID, input.Content); blocked != nil {
		return nil, blocked
	}
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) {
		return nil, ErrInvalidTemperature
	}
	conversation, err := s.repository.FindByIDForUser(ctx, userID, conversationID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	if err := s.gateway.Validate(input.Provider, input.Model); err != nil {
		return nil, &ProviderSelectionError{err: err}
	}
	var preparedAttachments PreparedAttachments
	if len(input.AttachmentIDs) > 0 {
		if s.attachments == nil {
			return nil, ErrAttachmentNotFound
		}
		preparedAttachments, err = s.attachments.PrepareForChat(ctx, userID, conversationID, input.Provider, input.Model, input.AttachmentIDs)
		if err != nil {
			return nil, err
		}
	}
	var responseFormat *provider.ResponseFormat
	if formatID := strings.TrimSpace(input.OutputFormat); formatID != "" {
		var ok bool
		responseFormat, ok = s.outputFormats.Get(formatID)
		if !ok {
			return nil, ErrOutputFormatNotFound
		}
		if err := s.gateway.ValidateResponseFormat(responseFormat); err != nil {
			return nil, err
		}
	}
	history, err := s.repository.ListByConversationForUser(ctx, userID, conversationID)
	if err != nil {
		return nil, err
	}

	operationID := llmgateway.OperationID(userID, conversationID, strings.TrimSpace(input.IdempotencyKey))
	if err := s.gateway.Begin(ctx, operationID); err != nil {
		return nil, err
	}
	session := &ChatSession{
		OperationID: operationID,
		UserID:      userID,
		UserMessage: model.Message{
			ConversationID:   conversationID,
			Role:             "user",
			Content:          input.Content,
			Status:           "complete",
			KnowledgeBaseIDs: model.JSONStringArray(uniqueStrings(input.KnowledgeBaseIDs)),
		},
		AssistantMessage: model.Message{
			ConversationID: conversationID,
			Role:           "assistant",
			Provider:       input.Provider,
			Model:          input.Model,
			Status:         "streaming",
		},
	}
	if branch := selectedBranch(history); len(branch) > 0 {
		parentID := branch[len(branch)-1].ID
		session.UserMessage.ParentMessageID = &parentID
	}
	requestMessages := make([]provider.Message, 0, 3)
	if system := strings.TrimSpace(s.systemPrompt); system != "" {
		requestMessages = append(requestMessages, provider.Message{Role: "system", Content: system})
	}
	requestMessages = append(requestMessages, provider.Message{Role: "user", Content: session.UserMessage.Content + preparedAttachments.DocumentText, Parts: preparedAttachments.Parts})
	request := provider.ChatRequest{Model: input.Model, Messages: requestMessages, Temperature: input.Temperature, ResponseFormat: responseFormat}
	if err := s.applyGrounding(ctx, session, session.UserMessage.Content, input.KnowledgeBaseIDs, &request); err != nil {
		s.gateway.Abandon(operationID)
		return nil, err
	}
	var createErr error
	if len(input.AttachmentIDs) > 0 {
		if repositoryWithAttachments, ok := s.repository.(interface {
			CreateTurnWithAttachments(context.Context, string, []string, *model.Message, *model.Message) error
		}); ok {
			createErr = repositoryWithAttachments.CreateTurnWithAttachments(ctx, userID, uniqueStrings(input.AttachmentIDs), &session.UserMessage, &session.AssistantMessage)
		} else {
			createErr = ErrAttachmentNotFound
		}
	} else {
		createErr = s.repository.CreateTurn(ctx, &session.UserMessage, &session.AssistantMessage)
	}
	if createErr != nil {
		s.gateway.Abandon(operationID)
		if errors.Is(createErr, domain.ErrNotFound) {
			return nil, ErrAttachmentNotReady
		}
		return nil, createErr
	}
	for index := range preparedAttachments.Items {
		messageID := session.UserMessage.ID
		preparedAttachments.Items[index].MessageID = &messageID
	}
	session.UserMessage.Attachments = preparedAttachments.Items

	if s.gateway.SupportsTools(input.Provider) && s.tools != nil {
		request.Tools = s.tools.Definitions()
	}
	if s.thinking != nil && s.thinking.Enabled && s.gateway.SupportsThinking(input.Provider, input.Model) {
		thinking := *s.thinking
		request.Thinking = &thinking
	}
	contextResult, contextErr := s.prepareContext(ctx, session, conversation, history, request)
	if contextErr != nil {
		usageData := model.MessageUsage{}
		if contextResult.usageReported {
			usageData.InputTokens = contextResult.usage.InputTokens
			usageData.OutputTokens = contextResult.usage.OutputTokens
			usageData.TotalTokens = contextResult.usage.TotalTokens
			usageData.UsageReported = true
			usageData.CostNanoUSD = contextResult.costNanoUSD
			usageData.CostKnown = contextResult.costKnown
		}
		finishReason := "context_preparation_failed"
		if errors.Is(contextErr, llmgateway.ErrTokenBudgetExceeded) {
			finishReason = "token_budget_exceeded"
		}
		_, _ = s.finalize(session.AssistantMessage.ID, model.MessageCompletion{Status: "error", FinishReason: finishReason, Usage: usageData})
		s.gateway.Abandon(operationID)
		return nil, contextErr
	}
	request = contextResult.request
	session.preparationUsage = contextResult.usage
	session.preparationUsageReported = contextResult.usageReported
	session.preparationCostNanoUSD = contextResult.costNanoUSD
	session.preparationCostKnown = contextResult.costKnown
	session.preparationCostTracked = contextResult.costTracked
	session.request = request
	if err := s.reserveQuota(ctx, session, input.Provider); err != nil {
		_, _ = s.finalize(session.AssistantMessage.ID, model.MessageCompletion{Status: "error", FinishReason: quotaFinishReason(err)})
		s.gateway.Abandon(operationID)
		return nil, err
	}
	return session, nil
}

// PrepareRegeneration creates only a new assistant version. The original user
// message is reused and the model context stops at that user message, so later
// turns and previous answer versions cannot leak into the regenerated answer.
func (s *ChatService) PrepareRegeneration(ctx context.Context, userID, conversationID, targetMessageID string, input RegenerateInput) (*ChatSession, error) {
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) {
		return nil, ErrInvalidTemperature
	}
	conversation, err := s.repository.FindByIDForUser(ctx, userID, conversationID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	target, err := s.repository.FindMessageByIDForUser(ctx, userID, conversationID, targetMessageID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrMessageNotFound
		}
		return nil, err
	}
	if target.Role != "assistant" || target.Status == "streaming" {
		return nil, ErrMessageNotRegenerable
	}
	if strings.TrimSpace(input.Provider) == "" {
		input.Provider = target.Provider
	}
	if strings.TrimSpace(input.Model) == "" {
		input.Model = target.Model
	}
	if err := s.gateway.Validate(input.Provider, input.Model); err != nil {
		return nil, &ProviderSelectionError{err: err}
	}
	var responseFormat *provider.ResponseFormat
	if formatID := strings.TrimSpace(input.OutputFormat); formatID != "" {
		var ok bool
		responseFormat, ok = s.outputFormats.Get(formatID)
		if !ok {
			return nil, ErrOutputFormatNotFound
		}
		if err := s.gateway.ValidateResponseFormat(responseFormat); err != nil {
			return nil, err
		}
	}
	history, err := s.repository.ListByConversationForUser(ctx, userID, conversationID)
	if err != nil {
		return nil, err
	}
	if messageIndex(history, target.ID) < 0 {
		return nil, ErrMessageNotFound
	}
	replyToID := ""
	if target.ReplyToMessageID != nil {
		replyToID = *target.ReplyToMessageID
	} else if target.ParentMessageID != nil {
		replyToID = *target.ParentMessageID
	}
	userIndex := messageIndex(history, replyToID)
	if userIndex < 0 {
		targetIndex := messageIndex(history, target.ID)
		for index := targetIndex - 1; index >= 0; index-- {
			if history[index].Role == "user" {
				userIndex = index
				replyToID = history[index].ID
				break
			}
		}
	}
	if userIndex < 0 {
		return nil, ErrMessageNotRegenerable
	}

	operationID := llmgateway.OperationID(userID, conversationID+":"+targetMessageID, strings.TrimSpace(input.IdempotencyKey))
	if err := s.gateway.Begin(ctx, operationID); err != nil {
		return nil, err
	}
	var generationKey *string
	if operationID != "" {
		generationKey = &operationID
	}
	session := &ChatSession{
		OperationID: operationID,
		UserID:      userID,
		UserMessage: history[userIndex],
		AssistantMessage: model.Message{
			ConversationID: conversationID,
			Role:           "assistant",
			Provider:       input.Provider,
			Model:          input.Model,
			Status:         "streaming",
			GenerationKey:  generationKey,
		},
		IsRegeneration:           true,
		ContextIsExplicitPath:    true,
		RegeneratedFromMessageID: targetMessageID,
	}
	var regeneratedAttachments PreparedAttachments
	if len(session.UserMessage.Attachments) > 0 && s.attachments != nil {
		regeneratedAttachments, err = s.attachments.PrepareStoredForChat(ctx, input.Provider, input.Model, session.UserMessage.Attachments)
		if err != nil {
			s.gateway.Abandon(operationID)
			return nil, err
		}
	}
	requestMessages := make([]provider.Message, 0, 3)
	if system := strings.TrimSpace(s.systemPrompt); system != "" {
		requestMessages = append(requestMessages, provider.Message{Role: "system", Content: system})
	}
	requestMessages = append(requestMessages, provider.Message{Role: "user", Content: session.UserMessage.Content + regeneratedAttachments.DocumentText, Parts: regeneratedAttachments.Parts})
	request := provider.ChatRequest{Model: input.Model, Messages: requestMessages, Temperature: input.Temperature, ResponseFormat: responseFormat}
	if err := s.applyGrounding(ctx, session, session.UserMessage.Content, []string(session.UserMessage.KnowledgeBaseIDs), &request); err != nil {
		s.gateway.Abandon(operationID)
		return nil, err
	}
	if err := s.repository.CreateRegeneration(ctx, userID, targetMessageID, replyToID, &session.AssistantMessage); err != nil {
		s.gateway.Abandon(operationID)
		if errors.Is(err, domain.ErrAlreadyExists) {
			return nil, llmgateway.ErrIdempotencyCompleted
		}
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrMessageNotFound
		}
		return nil, err
	}

	if s.gateway.SupportsTools(input.Provider) && s.tools != nil {
		request.Tools = s.tools.Definitions()
	}
	if s.thinking != nil && s.thinking.Enabled && s.gateway.SupportsThinking(input.Provider, input.Model) {
		thinking := *s.thinking
		request.Thinking = &thinking
	}
	// A stored summary may cover messages after the selected answer. Reusing it
	// would violate regeneration's historical cutoff, so rebuild from the exact
	// prefix before the original user message.
	conversation.Summary = ""
	conversation.SummaryThroughMessageID = nil
	contextResult, contextErr := s.prepareContext(ctx, session, conversation, ancestryBefore(history, history[userIndex]), request)
	if contextErr != nil {
		usageData := model.MessageUsage{}
		if contextResult.usageReported {
			usageData.InputTokens = contextResult.usage.InputTokens
			usageData.OutputTokens = contextResult.usage.OutputTokens
			usageData.TotalTokens = contextResult.usage.TotalTokens
			usageData.UsageReported = true
			usageData.CostNanoUSD = contextResult.costNanoUSD
			usageData.CostKnown = contextResult.costKnown
		}
		finishReason := "context_preparation_failed"
		if errors.Is(contextErr, llmgateway.ErrTokenBudgetExceeded) {
			finishReason = "token_budget_exceeded"
		}
		_, _ = s.finalize(session.AssistantMessage.ID, model.MessageCompletion{Status: "error", FinishReason: finishReason, Usage: usageData})
		s.gateway.Abandon(operationID)
		return nil, contextErr
	}
	session.preparationUsage = contextResult.usage
	session.preparationUsageReported = contextResult.usageReported
	session.preparationCostNanoUSD = contextResult.costNanoUSD
	session.preparationCostKnown = contextResult.costKnown
	session.preparationCostTracked = contextResult.costTracked
	session.request = contextResult.request
	if err := s.reserveQuota(ctx, session, input.Provider); err != nil {
		_, _ = s.finalize(session.AssistantMessage.ID, model.MessageCompletion{Status: "error", FinishReason: quotaFinishReason(err)})
		s.gateway.Abandon(operationID)
		return nil, err
	}
	return session, nil
}

// PrepareEdit creates a sibling user node and a fresh assistant child. The
// original user message and its complete descendant subtree remain unchanged.
func (s *ChatService) PrepareEdit(ctx context.Context, userID, conversationID, targetMessageID string, input ChatInput) (*ChatSession, error) {
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || len([]rune(input.Content)) > s.messageLimit() {
		return nil, ErrInvalidMessageLength
	}
	if blocked := s.checkInputModeration(ctx, userID, input.Content); blocked != nil {
		return nil, blocked
	}
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) {
		return nil, ErrInvalidTemperature
	}
	conversation, err := s.repository.FindByIDForUser(ctx, userID, conversationID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	target, err := s.repository.FindMessageByIDForUser(ctx, userID, conversationID, targetMessageID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrMessageNotFound
		}
		return nil, err
	}
	if target.Role != "user" || target.Status != "complete" {
		return nil, ErrMessageNotEditable
	}
	if err := s.gateway.Validate(input.Provider, input.Model); err != nil {
		return nil, &ProviderSelectionError{err: err}
	}
	var responseFormat *provider.ResponseFormat
	if formatID := strings.TrimSpace(input.OutputFormat); formatID != "" {
		var ok bool
		responseFormat, ok = s.outputFormats.Get(formatID)
		if !ok {
			return nil, ErrOutputFormatNotFound
		}
		if err := s.gateway.ValidateResponseFormat(responseFormat); err != nil {
			return nil, err
		}
	}
	history, err := s.repository.ListByConversationForUser(ctx, userID, conversationID)
	if err != nil {
		return nil, err
	}
	if messageIndex(history, target.ID) < 0 {
		return nil, ErrMessageNotFound
	}
	knowledgeBaseIDs := input.KnowledgeBaseIDs
	if knowledgeBaseIDs == nil {
		knowledgeBaseIDs = []string(target.KnowledgeBaseIDs)
	}
	operationID := llmgateway.OperationID(userID, conversationID+":edit:"+targetMessageID, strings.TrimSpace(input.IdempotencyKey))
	if err := s.gateway.Begin(ctx, operationID); err != nil {
		return nil, err
	}
	var generationKey *string
	if operationID != "" {
		generationKey = &operationID
	}
	session := &ChatSession{
		OperationID: operationID,
		UserID:      userID,
		UserMessage: model.Message{
			ConversationID:   conversationID,
			Role:             "user",
			Content:          input.Content,
			Status:           "complete",
			ParentMessageID:  target.ParentMessageID,
			KnowledgeBaseIDs: model.JSONStringArray(uniqueStrings(knowledgeBaseIDs)),
		},
		AssistantMessage: model.Message{
			ConversationID: conversationID,
			Role:           "assistant",
			Provider:       input.Provider,
			Model:          input.Model,
			Status:         "streaming",
			GenerationKey:  generationKey,
		},
		IsEdit:                true,
		ContextIsExplicitPath: true,
	}
	requestMessages := make([]provider.Message, 0, 3)
	if system := strings.TrimSpace(s.systemPrompt); system != "" {
		requestMessages = append(requestMessages, provider.Message{Role: "system", Content: system})
	}
	requestMessages = append(requestMessages, provider.Message{Role: "user", Content: session.UserMessage.Content})
	request := provider.ChatRequest{Model: input.Model, Messages: requestMessages, Temperature: input.Temperature, ResponseFormat: responseFormat}
	if err := s.applyGrounding(ctx, session, session.UserMessage.Content, knowledgeBaseIDs, &request); err != nil {
		s.gateway.Abandon(operationID)
		return nil, err
	}
	if err := s.repository.CreateEditedTurn(ctx, userID, targetMessageID, &session.UserMessage, &session.AssistantMessage); err != nil {
		s.gateway.Abandon(operationID)
		if errors.Is(err, domain.ErrAlreadyExists) {
			return nil, llmgateway.ErrIdempotencyCompleted
		}
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrMessageNotFound
		}
		return nil, err
	}

	if s.gateway.SupportsTools(input.Provider) && s.tools != nil {
		request.Tools = s.tools.Definitions()
	}
	if s.thinking != nil && s.thinking.Enabled && s.gateway.SupportsThinking(input.Provider, input.Model) {
		thinking := *s.thinking
		request.Thinking = &thinking
	}
	conversation.Summary = ""
	conversation.SummaryThroughMessageID = nil
	contextResult, contextErr := s.prepareContext(ctx, session, conversation, ancestryBefore(history, target), request)
	if contextErr != nil {
		usageData := model.MessageUsage{}
		if contextResult.usageReported {
			usageData = model.MessageUsage{
				InputTokens: contextResult.usage.InputTokens, OutputTokens: contextResult.usage.OutputTokens,
				TotalTokens: contextResult.usage.TotalTokens, UsageReported: true,
				CostNanoUSD: contextResult.costNanoUSD, CostKnown: contextResult.costKnown,
			}
		}
		finishReason := "context_preparation_failed"
		if errors.Is(contextErr, llmgateway.ErrTokenBudgetExceeded) {
			finishReason = "token_budget_exceeded"
		}
		_, _ = s.finalize(session.AssistantMessage.ID, model.MessageCompletion{Status: "error", FinishReason: finishReason, Usage: usageData})
		s.gateway.Abandon(operationID)
		return nil, contextErr
	}
	session.preparationUsage = contextResult.usage
	session.preparationUsageReported = contextResult.usageReported
	session.preparationCostNanoUSD = contextResult.costNanoUSD
	session.preparationCostKnown = contextResult.costKnown
	session.preparationCostTracked = contextResult.costTracked
	session.request = contextResult.request
	if err := s.reserveQuota(ctx, session, input.Provider); err != nil {
		_, _ = s.finalize(session.AssistantMessage.ID, model.MessageCompletion{Status: "error", FinishReason: quotaFinishReason(err)})
		s.gateway.Abandon(operationID)
		return nil, err
	}
	return session, nil
}

func messageIndex(messages []model.Message, id string) int {
	if id == "" {
		return -1
	}
	for index := range messages {
		if messages[index].ID == id {
			return index
		}
	}
	return -1
}

// 业务编排层，负责多轮模型调用、工具执行、内容聚合、用量统计和数据库落库。
func (s *ChatService) Stream(ctx context.Context, session *ChatSession, onEvent func(provider.StreamEvent)) (model.Message, string, error) {
	var answer strings.Builder
	var think strings.Builder
	usage := session.preparationUsage
	usageReported := session.preparationUsageReported
	var conversationUsage provider.TokenUsage
	conversationUsageReported := false
	status := "complete"
	finishReason := ""
	var structuredOutput json.RawMessage
	messages := append([]provider.Message(nil), session.request.Messages...)
	toolCalls := 0
	modelTurn := 0
	streamCtx := ctx
	cancelStream := func() {}
	if s.stream.TotalTimeout > 0 {
		streamCtx, cancelStream = context.WithTimeout(ctx, s.stream.TotalTimeout)
	}
	defer cancelStream()

	for {
		modelTurn++
		request := session.request
		request.Messages = messages
		turn := s.consumeProviderStream(ctx, streamCtx, llmgateway.Request{
			OperationID: session.OperationID, UserID: session.UserID,
			Provider: session.AssistantMessage.Provider, Model: session.AssistantMessage.Model,
			Turn: modelTurn, ChatRequest: request,
		}, onEvent)
		answer.WriteString(turn.answer)
		think.WriteString(turn.think)
		if turn.usageReported {
			usage.Add(turn.usage)
			conversationUsage.Add(turn.usage)
			usageReported = true
			conversationUsageReported = true
		}
		finishReason = turn.finishReason
		if len(turn.structuredOutput) > 0 {
			structuredOutput = turn.structuredOutput
		}
		if turn.err != nil {
			kind := provider.StreamErrorKindOf(turn.err)
			logger.Warn(ctx, "model stream interrupted",
				logger.String("provider", session.AssistantMessage.Provider),
				logger.String("model", session.AssistantMessage.Model),
				logger.String("reason", string(kind)),
				logger.Err(turn.err),
			)
			finishReason = string(kind)
			if kind == provider.StreamErrorCanceled {
				status = "stopped"
			} else if kind == provider.StreamErrorUpstream {
				status = "error"
				onEvent(provider.StreamEvent{Err: turn.err})
			} else {
				status = "interrupted"
				onEvent(provider.StreamEvent{Err: turn.err})
			}
			break
		}
		if len(turn.toolCalls) == 0 {
			break
		}
		if s.tools == nil {
			status = "error"
			finishReason = "tool_registry_unavailable"
			onEvent(provider.StreamEvent{Err: errors.New("模型请求了工具，但服务端未启用工具注册表")})
			break
		}
		if toolCalls >= s.maxToolCalls {
			status = "error"
			finishReason = "tool_iteration_limit"
			onEvent(provider.StreamEvent{Err: fmt.Errorf("工具调用超过最大轮次 %d", s.maxToolCalls)})
			break
		}

		toolCalls++
		// 执行本轮模型请求的工具：把 assistant（含 tool_calls）和各 tool 结果追加进 messages，
		// 供下一轮 ChatStream 继续对话。
		messages = s.executeToolCalls(streamCtx, session.UserID, messages, turn.answer, turn.think, turn.toolCalls)
	}
	if ctx.Err() != nil {
		status = "stopped"
		finishReason = string(provider.StreamErrorCanceled)
	} else if errors.Is(streamCtx.Err(), context.DeadlineExceeded) && status == "complete" {
		status = "interrupted"
		finishReason = string(provider.StreamErrorTotalTimeout)
	} else if status == "complete" && finishReason == "" {
		finishReason = "stop"
	}
	s.moderateOutput(ctx, session, &answer, &think, &structuredOutput, &status, &finishReason)
	var citations []model.MessageCitation
	if len(session.UserMessage.KnowledgeBaseIDs) > 0 {
		filtered, validated := validatedCitations(answer.String(), session.groundingMatches)
		answer.Reset()
		answer.WriteString(filtered)
		citations = validated
	}
	usageData := model.MessageUsage{
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		TotalTokens:   usage.TotalTokens,
		UsageReported: usageReported,
	}
	s.applyConversationCost(session, &usageData, conversationUsage, conversationUsageReported)
	s.settleQuota(session, &usageData, status)
	s.alertCostAnomaly(ctx, session, usageData)
	completed, err := s.finalize(session.AssistantMessage.ID, model.MessageCompletion{
		Content: answer.String(), Think: think.String(), StructuredOutput: model.JSONDocument(structuredOutput), Status: status, FinishReason: finishReason, Usage: usageData,
		Citations: citations,
	})
	if err != nil {
		s.gateway.Abandon(session.OperationID)
	} else {
		s.gateway.Complete(session.OperationID)
	}
	return completed, status, err
}

type streamTurn struct {
	answer           string
	think            string
	toolCalls        []provider.ToolCall
	usage            provider.TokenUsage
	usageReported    bool
	finishReason     string
	structuredOutput json.RawMessage
	err              error
}

func (s *ChatService) consumeProviderStream(parentCtx, streamCtx context.Context, request llmgateway.Request, onEvent func(provider.StreamEvent)) streamTurn {
	callCtx, cancel := context.WithCancel(streamCtx)
	defer cancel()
	events := s.gateway.ChatStream(callCtx, request)
	var answer, think strings.Builder
	result := streamTurn{toolCalls: make([]provider.ToolCall, 0)}
	var ttftTimer *time.Timer
	var ttft <-chan time.Time
	firstEventReceived := false
	var connectedAt time.Time

	stopTTFT := func() {
		if ttftTimer != nil && !ttftTimer.Stop() {
			select {
			case <-ttftTimer.C:
			default:
			}
		}
		ttft = nil
	}
	defer stopTTFT()

	for {
		select {
		case <-parentCtx.Done():
			result.err = provider.NewStreamError(provider.StreamErrorCanceled, parentCtx.Err())
			result.answer, result.think = answer.String(), think.String()
			return result
		case <-streamCtx.Done():
			kind := provider.StreamErrorTotalTimeout
			if parentCtx.Err() != nil {
				kind = provider.StreamErrorCanceled
			}
			result.err = provider.NewStreamError(kind, streamCtx.Err())
			result.answer, result.think = answer.String(), think.String()
			return result
		case <-ttft:
			result.err = provider.NewStreamError(provider.StreamErrorTTFTTimeout, errors.New("等待模型首个事件超时"))
			result.answer, result.think = answer.String(), think.String()
			return result
		case event, ok := <-events:
			if !ok {
				switch {
				case parentCtx.Err() != nil:
					result.err = provider.NewStreamError(provider.StreamErrorCanceled, parentCtx.Err())
				case errors.Is(streamCtx.Err(), context.DeadlineExceeded):
					result.err = provider.NewStreamError(provider.StreamErrorTotalTimeout, streamCtx.Err())
				default:
					result.err = provider.NewStreamError(provider.StreamErrorDisconnected, errors.New("模型流未收到正常结束标记"))
				}
				result.answer, result.think = answer.String(), think.String()
				return result
			}
			if event.Connected && !firstEventReceived && s.stream.TTFTTimeout > 0 {
				connectedAt = time.Now()
				stopTTFT()
				ttftTimer = time.NewTimer(s.stream.TTFTTimeout)
				ttft = ttftTimer.C
			}
			if event.Activity && !firstEventReceived {
				firstEventReceived = true
				stopTTFT()
				if !connectedAt.IsZero() {
					logger.Info(parentCtx, "model stream first event received", logger.Int64("ttft_ms", time.Since(connectedAt).Milliseconds()))
				}
			}
			if event.Err != nil {
				result.err = provider.ClassifyStreamError(parentCtx, event.Err)
				result.answer, result.think = answer.String(), think.String()
				return result
			}
			if event.Delta != "" {
				answer.WriteString(event.Delta)
				onEvent(provider.StreamEvent{Delta: event.Delta, Sequence: event.Sequence})
			}
			if event.ThinkDelta != "" {
				think.WriteString(event.ThinkDelta)
				onEvent(provider.StreamEvent{ThinkDelta: event.ThinkDelta, Sequence: event.Sequence})
			}
			if event.Usage != nil {
				result.usage.Merge(*event.Usage)
				result.usageReported = true
			}
			if len(event.ToolCalls) > 0 {
				result.toolCalls = append(result.toolCalls, event.ToolCalls...)
			}
			if event.Done {
				result.answer, result.think = answer.String(), think.String()
				result.finishReason = event.FinishReason
				result.structuredOutput = event.StructuredOutput
				return result
			}
		}
	}
}

// Chat runs a non-streaming request, including the same server-side tool loop
// as Stream, and returns content plus model reasoning as one complete result.
func (s *ChatService) Chat(ctx context.Context, session *ChatSession) (model.Message, error) {
	var answer, think strings.Builder
	usage := session.preparationUsage
	usageReported := session.preparationUsageReported
	var conversationUsage provider.TokenUsage
	conversationUsageReported := false
	status := "complete"
	var chatErr error
	var structuredOutput json.RawMessage
	messages := append([]provider.Message(nil), session.request.Messages...)
	toolCalls := 0
	modelTurn := 0

	for {
		modelTurn++
		request := session.request
		request.Messages = messages
		result, err := s.gateway.Chat(ctx, llmgateway.Request{
			OperationID: session.OperationID, UserID: session.UserID,
			Provider: session.AssistantMessage.Provider, Model: session.AssistantMessage.Model,
			Turn: modelTurn, ChatRequest: request,
		})
		if result.Usage != nil {
			usage.Add(*result.Usage)
			conversationUsage.Add(*result.Usage)
			usageReported = true
			conversationUsageReported = true
		}
		if err != nil {
			status = "error"
			chatErr = err
			break
		}
		answer.WriteString(result.Content)
		think.WriteString(result.Think)
		if len(result.StructuredOutput) > 0 {
			structuredOutput = result.StructuredOutput
		}
		if ctx.Err() != nil || len(result.ToolCalls) == 0 {
			break
		}
		if s.tools == nil {
			status = "error"
			chatErr = errors.New("模型请求了工具，但服务端未启用工具注册表")
			break
		}
		if toolCalls >= s.maxToolCalls {
			status = "error"
			chatErr = fmt.Errorf("工具调用超过最大轮次 %d", s.maxToolCalls)
			break
		}
		toolCalls++
		messages = s.executeToolCalls(ctx, session.UserID, messages, result.Content, result.Think, result.ToolCalls)
	}
	if ctx.Err() != nil {
		status = "stopped"
		if chatErr == nil {
			chatErr = ctx.Err()
		}
	}
	finishReason := "stop"
	if status != "complete" {
		finishReason = status
	}
	s.moderateOutput(ctx, session, &answer, &think, &structuredOutput, &status, &finishReason)
	var citations []model.MessageCitation
	if len(session.UserMessage.KnowledgeBaseIDs) > 0 {
		filtered, validated := validatedCitations(answer.String(), session.groundingMatches)
		answer.Reset()
		answer.WriteString(filtered)
		citations = validated
	}
	usageData := model.MessageUsage{
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		TotalTokens:   usage.TotalTokens,
		UsageReported: usageReported,
	}
	s.applyConversationCost(session, &usageData, conversationUsage, conversationUsageReported)
	s.settleQuota(session, &usageData, status)
	s.alertCostAnomaly(ctx, session, usageData)
	completed, finalizeErr := s.finalize(session.AssistantMessage.ID, model.MessageCompletion{
		Content: answer.String(), Think: think.String(), StructuredOutput: model.JSONDocument(structuredOutput), Status: status, FinishReason: finishReason, Usage: usageData,
		Citations: citations,
	})
	if finalizeErr != nil {
		s.gateway.Abandon(session.OperationID)
		return model.Message{}, finalizeErr
	}
	s.gateway.Complete(session.OperationID)
	if chatErr != nil {
		return completed, chatErr
	}
	return completed, nil
}

func (s *ChatService) applyConversationCost(session *ChatSession, usageData *model.MessageUsage, conversationUsage provider.TokenUsage, conversationUsageReported bool) {
	usageData.CostNanoUSD = session.preparationCostNanoUSD
	if !conversationUsageReported {
		// A normal answer call was attempted but did not report usage, so the
		// complete business-message cost cannot be declared known.
		usageData.CostKnown = false
		return
	}
	conversationCost, conversationCostKnown := s.gateway.CalculateCost(
		session.AssistantMessage.Provider, session.AssistantMessage.Model, conversationUsage,
	)
	usageData.CostNanoUSD += conversationCost
	if session.preparationCostTracked {
		usageData.CostKnown = session.preparationCostKnown && conversationCostKnown
	} else {
		usageData.CostKnown = conversationCostKnown
	}
}

// finalize is the only deliberately detached operation in the stream
// lifecycle. It has a strict deadline and only persists the terminal state so
// canceled requests do not leave messages stuck as "streaming".
func (s *ChatService) finalize(messageID string, completion model.MessageCompletion) (model.Message, error) {
	timeout := s.stream.FinalizeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.repository.Finalize(ctx, messageID, completion)
}

func (s *ChatService) executeToolCalls(ctx context.Context, userID string, messages []provider.Message, assistantContent, assistantThink string, calls []provider.ToolCall) []provider.Message {
	messages = append(messages, provider.Message{Role: "assistant", Content: assistantContent, ReasoningContent: assistantThink, ToolCalls: calls})
	// 循环执行工具调用：把 tool 执行结果追加进 messages，供下一轮 ChatStream 继续对话。
	for _, call := range calls {
		if ctx.Err() != nil {
			break
		}
		logger.Info(ctx, "tool execution started",
			logger.String("tool_call_id", call.ID),
			logger.String("tool_name", call.Function.Name),
		)
		content, err := s.tools.ExecuteForUser(ctx, userID, call)
		if err != nil {
			logger.Warn(ctx, "tool execution failed",
				logger.String("tool_call_id", call.ID),
				logger.String("tool_name", call.Function.Name),
				logger.Err(err),
			)
			content = toolErrorResult(err)
		} else {
			logger.Info(ctx, "tool execution completed",
				logger.String("tool_call_id", call.ID),
				logger.String("tool_name", call.Function.Name),
			)
		}
		messages = append(messages, provider.Message{Role: "tool", ToolCallID: call.ID, Content: content})
	}
	return messages
}

func toolErrorResult(err error) string {
	payload, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: err.Error()})
	return string(payload)
}

// checkInputModeration 在用户内容落库和调用模型之前执行输入审核。
// 命中策略时不创建任何消息，直接拒绝请求。
func (s *ChatService) checkInputModeration(ctx context.Context, userID, content string) error {
	if s.moderator == nil || content == "" {
		return nil
	}
	decision := s.moderator.Check(moderation.DirectionInput, content)
	if !decision.Blocked {
		return nil
	}
	s.alertModeration(ctx, userID, string(moderation.DirectionInput), decision.Term)
	return ErrContentBlockedByModeration
}

// moderateOutput 在落库前审核最终回答。命中策略时丢弃模型原文，
// 替换为固定提示文案并标记 finish_reason，避免违规内容进入持久化。
func (s *ChatService) moderateOutput(ctx context.Context, session *ChatSession, answer, think *strings.Builder, structuredOutput *json.RawMessage, status, finishReason *string) {
	if s.moderator == nil || answer.Len() == 0 {
		return
	}
	if *status != "complete" && *status != "stopped" {
		return
	}
	decision := s.moderator.Check(moderation.DirectionOutput, answer.String())
	if !decision.Blocked {
		return
	}
	answer.Reset()
	answer.WriteString(moderation.BlockedReply)
	think.Reset()
	*structuredOutput = nil
	*finishReason = "moderation_blocked"
	s.alertModeration(ctx, session.UserID, string(moderation.DirectionOutput), decision.Term)
}

func (s *ChatService) alertModeration(ctx context.Context, userID, direction, term string) {
	if s.alerts == nil {
		return
	}
	s.alerts.Alert(ctx, alerting.KindModerationHit,
		logger.String("user_id", userID),
		logger.String("direction", direction),
		logger.String("term", term),
	)
}

// reserveQuota 在调用模型前按“估算输入 + 最大输出预留”预扣额度。
// 预扣失败时不会发起任何计费调用，消息直接以 error 终态落库。
func (s *ChatService) reserveQuota(ctx context.Context, session *ChatSession, providerID string) error {
	if s.quotas == nil {
		return nil
	}
	budget, err := s.gateway.TokenBudget(providerID, session.AssistantMessage.Model, 0)
	if err != nil {
		return err
	}
	inputTokens, err := s.gateway.EstimateInputTokens(providerID, session.request)
	if err != nil {
		return err
	}
	estimate := inputTokens + budget.MaxOutputTokens
	if err := s.quotas.Reserve(ctx, session.UserID, estimate); err != nil {
		if s.alerts != nil && (errors.Is(err, ErrDailyQuotaExceeded) || errors.Is(err, ErrMonthlyQuotaExceeded) || errors.Is(err, ErrTooManyConcurrentGenerations)) {
			s.alerts.Alert(ctx, alerting.KindQuotaExhausted,
				logger.String("user_id", session.UserID),
				logger.String("reason", err.Error()),
				logger.Int64("estimated_tokens", estimate),
			)
		}
		return err
	}
	session.quotaReservedTokens = estimate
	return nil
}

// settleQuota 按实际用量结算预扣额度。结算刻意使用独立的短超时上下文，
// 客户端取消或服务端超时不会导致预扣永久泄漏。
func (s *ChatService) settleQuota(session *ChatSession, usageData *model.MessageUsage, status string) {
	if s.quotas == nil || session.quotaReservedTokens <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	switch {
	case usageData.UsageReported:
		err = s.quotas.Settle(ctx, session.UserID, session.quotaReservedTokens, usageData.TotalTokens)
	case status == "complete":
		// 用量未知但回答已完成：按预扣值结算，宁可多扣不漏扣。
		err = s.quotas.Settle(ctx, session.UserID, session.quotaReservedTokens, session.quotaReservedTokens)
	default:
		// 请求失败且没有可用用量：补偿预扣额度。
		err = s.quotas.Release(ctx, session.UserID, session.quotaReservedTokens)
	}
	if err != nil {
		logger.Error(ctx, "quota settlement failed", logger.Err(err), logger.String("user_id", session.UserID))
	}
	session.quotaReservedTokens = 0
}

func (s *ChatService) alertCostAnomaly(ctx context.Context, session *ChatSession, usageData model.MessageUsage) {
	if s.alerts == nil || s.costAlertNanoUSD <= 0 {
		return
	}
	if !usageData.CostKnown || usageData.CostNanoUSD < s.costAlertNanoUSD {
		return
	}
	s.alerts.Alert(ctx, alerting.KindCostAnomaly,
		logger.String("user_id", session.UserID),
		logger.String("assistant_message_id", session.AssistantMessage.ID),
		logger.Int64("cost_nano_usd", usageData.CostNanoUSD),
		logger.Int64("total_tokens", usageData.TotalTokens),
	)
}

func quotaFinishReason(err error) string {
	switch {
	case errors.Is(err, ErrDailyQuotaExceeded), errors.Is(err, ErrMonthlyQuotaExceeded):
		return "quota_exceeded"
	case errors.Is(err, ErrTooManyConcurrentGenerations):
		return "concurrency_limit"
	}
	return "quota_error"
}

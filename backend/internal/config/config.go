package config

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

type ProviderConfig struct {
	ID                 string                  `json:"id"`
	Label              string                  `json:"label"`
	Type               string                  `json:"type"`
	BaseURL            string                  `json:"baseUrl"`
	APIKey             string                  `json:"-"`
	Models             []string                `json:"models"`
	StreamIncludeUsage bool                    `json:"-"`
	ModelPricing       map[string]ModelPricing `json:"-"`
	ModelLimits        map[string]ModelLimits  `json:"-"`
	ModelTokenizers    map[string]string       `json:"-"`
	VisionModels       map[string]bool         `json:"-"`
	ConnectTimeout     time.Duration           `json:"-"`
}

// ModelLimits 定义部署时的安全限制。ContextWindow 包含输入和输出 Token，
// MaxOutputTokens 表示单次调用允许预留的最大输出 Token 数。
type ModelLimits struct {
	ContextWindow   int64
	MaxOutputTokens int64
}

// ModelPricing 以每百万 Token 的美元价格计价。
// 供应商报告实际用量，部署方在配置中维护适用的计费价格。
type ModelPricing struct {
	InputUSDPerMillionTokens  float64
	OutputUSDPerMillionTokens float64
}

// EmbeddingConfig 描述当前启用的兼容 OpenAI 协议的 Embedding 部署。
// Embedding 模型的端点、维度、批量限制和计费方式独立于对话模型配置。
type EmbeddingConfig struct {
	Enabled                  bool
	Provider                 string
	BaseURL                  string
	APIKey                   string
	Model                    string
	Dimension                int
	SendDimensions           bool
	Tokenizer                string
	MaxInputs                int
	BatchSize                int
	MaxInputTokens           int64
	MaxBatchTokens           int64
	ConnectTimeout           time.Duration
	RequestTimeout           time.Duration
	MaxRetries               int
	RetryBackoff             time.Duration
	CacheTTL                 time.Duration
	InputUSDPerMillionTokens *float64
}

// KnowledgeConfig 控制知识文档摄取与检索，独立于单次对话附件的配置。
// 其中的 Token 数量由 Embedding 分词器估算。
type KnowledgeConfig struct {
	DocumentMaxBytes             int64
	MaxTextChars                 int
	ChunkTargetTokens            int64
	ChunkOverlap                 int64
	ChunkMinTokens               int64
	EmbeddingBatch               int
	IngestionTimeout             time.Duration
	RetrievalCandidateLimit      int
	RetrievalTopK                int
	RetrievalSimilarityThreshold float64
	RetrievalContextTokenBudget  int64
	RetrievalMaxKnowledgeBases   int
	RetrievalMaxQueryChars       int
	RetrievalTraceEnabled        bool
	RetrievalMode                string
	RetrievalRRFK                int
}

type Config struct {
	HTTPAddr                    string
	DatabaseURL                 string
	DatabaseMaxOpenConns        int
	DatabaseMaxIdleConns        int
	DatabaseConnMaxLifetime     time.Duration
	DatabaseConnMaxIdleTime     time.Duration
	RedisURL                    string
	ObjectStorageEndpoint       string
	ObjectStorageAccessKey      string
	ObjectStorageSecretKey      string
	ObjectStorageBucket         string
	ObjectStorageRegion         string
	ObjectStorageUseSSL         bool
	AttachmentMaxBytes          int64
	AttachmentMaxCount          int
	AttachmentMaxTextChars      int
	JWTSecret                   string
	AccessTokenTTL              time.Duration
	RefreshTokenTTL             time.Duration
	RefreshCookieName           string
	RefreshCookieSecure         bool
	EmailVerificationTTL        time.Duration
	PasswordResetTTL            time.Duration
	PublicWebURL                string
	EmailDeliveryMode           string
	SMTPHost                    string
	SMTPPort                    int
	SMTPUsername                string
	SMTPPassword                string
	SMTPFrom                    string
	AuthRateLimitWindow         time.Duration
	AuthLoginRateLimit          int
	AuthRegisterRateLimit       int
	AuthPasswordResetRateLimit  int
	AuthCredentialRetentionDays int
	AccountAuditRetentionDays   int
	CORSAllowedOrigins          []string
	LogLevel                    string
	LogDir                      string
	LogRetentionDays            int
	MaxToolIterations           int
	StreamConnectTimeout        time.Duration
	StreamTTFTTimeout           time.Duration
	StreamTotalTimeout          time.Duration
	StreamCacheTTL              time.Duration
	StreamCacheMaxEvents        int
	UserEventTTL                time.Duration
	UserEventMaxEvents          int
	EnableThinking              bool
	ThinkingBudgetTokens        int
	LLMTokenBudget              int64
	LLMMaxOutputTokens          int64
	LLMRateTokensPerMinute      int64
	LLMRateBurst                int64
	LLMIdempotencyTTL           time.Duration
	LLMSyncMaxRetries           int
	LLMRetryBackoff             time.Duration
	ContextSummaryEnabled       bool
	ContextSummaryProvider      string
	ContextSummaryModel         string
	ContextSummaryMaxTokens     int64
	ContextKeepRecentMessages   int
	ContextSummaryMaxCalls      int
	ContextQualityEnabled       bool
	ContextQualityThreshold     int
	ContextQualityMaxTokens     int64
	TitleGenerationProvider     string
	TitleGenerationModel        string
	TitleGenerationMaxTokens    int64
	TitleGenerationMaxLength    int
	TitleGenerationTimeout      time.Duration
	SystemPrompt                string
	QuotaEnabled                bool
	QuotaDefaultDailyTokens     int64
	QuotaDefaultMonthlyTokens   int64
	QuotaMaxConcurrent          int
	ChatRateLimit               int
	ChatRateLimitWindow         time.Duration
	ChatBodyMaxBytes            int64
	MessageMaxChars             int
	ToolArgumentMaxBytes        int
	ToolResultMaxBytes          int
	ToolExecutionTimeout        time.Duration
	ToolAdminOnly               []string
	ModerationEnabled           bool
	ModerationInputBlocklist    []string
	ModerationOutputBlocklist   []string
	AdminEmails                 []string
	AlertCostThresholdNanoUSD   int64
	Embedding                   EmbeddingConfig
	Knowledge                   KnowledgeConfig
	Rerank                      RerankConfig
	QueryRewrite                QueryRewriteConfig
	Providers                   []ProviderConfig
}

func Load() (Config, error) {
	// 本地开发支持读取根目录的 .env；容器或生产环境中已设置的环境变量优先。
	for _, path := range []string{".env", "../.env"} {
		if err := loadDotEnv(path); err != nil {
			return Config{}, err
		}
	}

	accessTokenTTL, err := positiveDurationEnv("ACCESS_TOKEN_TTL", "15m")
	if err != nil {
		return Config{}, err
	}
	refreshTokenTTL, err := positiveDurationEnv("REFRESH_TOKEN_TTL", "720h")
	if err != nil {
		return Config{}, err
	}
	emailVerificationTTL, err := positiveDurationEnv("EMAIL_VERIFICATION_TTL", "24h")
	if err != nil {
		return Config{}, err
	}
	passwordResetTTL, err := positiveDurationEnv("PASSWORD_RESET_TTL", "30m")
	if err != nil {
		return Config{}, err
	}
	authRateLimitWindow, err := positiveDurationEnv("AUTH_RATE_LIMIT_WINDOW", "15m")
	if err != nil {
		return Config{}, err
	}
	authLoginRateLimit, err := positiveIntEnv("AUTH_LOGIN_RATE_LIMIT", 10)
	if err != nil {
		return Config{}, err
	}
	authRegisterRateLimit, err := positiveIntEnv("AUTH_REGISTER_RATE_LIMIT", 5)
	if err != nil {
		return Config{}, err
	}
	authPasswordResetRateLimit, err := positiveIntEnv("AUTH_PASSWORD_RESET_RATE_LIMIT", 5)
	if err != nil {
		return Config{}, err
	}
	authCredentialRetentionDays, err := positiveIntEnv("AUTH_CREDENTIAL_RETENTION_DAYS", 30)
	if err != nil {
		return Config{}, err
	}
	accountAuditRetentionDays, err := positiveIntEnv("ACCOUNT_AUDIT_RETENTION_DAYS", 365)
	if err != nil {
		return Config{}, err
	}
	refreshCookieSecure, err := boolEnv("REFRESH_COOKIE_SECURE", false)
	if err != nil {
		return Config{}, err
	}
	smtpPort, err := positiveIntEnv("SMTP_PORT", 587)
	if err != nil {
		return Config{}, err
	}
	databaseMaxOpenConns, err := positiveIntEnv("DATABASE_MAX_OPEN_CONNS", 20)
	if err != nil {
		return Config{}, err
	}
	databaseMaxIdleConns, err := positiveIntEnv("DATABASE_MAX_IDLE_CONNS", 10)
	if err != nil {
		return Config{}, err
	}
	databaseConnMaxLifetime, err := time.ParseDuration(env("DATABASE_CONN_MAX_LIFETIME", "1h"))
	if err != nil {
		return Config{}, fmt.Errorf("parse DATABASE_CONN_MAX_LIFETIME: %w", err)
	}
	databaseConnMaxIdleTime, err := time.ParseDuration(env("DATABASE_CONN_MAX_IDLE_TIME", "10m"))
	if err != nil {
		return Config{}, fmt.Errorf("parse DATABASE_CONN_MAX_IDLE_TIME: %w", err)
	}
	maxToolIterations, err := positiveIntEnv("MAX_TOOL_ITERATIONS", 4)
	if err != nil {
		return Config{}, err
	}
	if maxToolIterations > 10 {
		return Config{}, fmt.Errorf("MAX_TOOL_ITERATIONS must not exceed 10")
	}
	logRetentionDays, err := positiveIntEnv("LOG_RETENTION_DAYS", 30)
	if err != nil {
		return Config{}, err
	}
	streamConnectTimeout, err := positiveDurationEnv("STREAM_CONNECT_TIMEOUT", "10s")
	if err != nil {
		return Config{}, err
	}
	streamTTFTTimeout, err := positiveDurationEnv("STREAM_TTFT_TIMEOUT", "30s")
	if err != nil {
		return Config{}, err
	}
	streamTotalTimeout, err := positiveDurationEnv("STREAM_TOTAL_TIMEOUT", "5m")
	if err != nil {
		return Config{}, err
	}
	streamCacheTTL, err := positiveDurationEnv("STREAM_CACHE_TTL", "5m")
	if err != nil {
		return Config{}, err
	}
	streamCacheMaxEvents, err := positiveIntEnv("STREAM_CACHE_MAX_EVENTS", 10_000)
	if err != nil {
		return Config{}, err
	}
	userEventTTL, err := positiveDurationEnv("USER_EVENT_TTL", "10m")
	if err != nil {
		return Config{}, err
	}
	userEventMaxEvents, err := positiveIntEnv("USER_EVENT_MAX_EVENTS", 1_000)
	if err != nil {
		return Config{}, err
	}
	enableThinking, err := boolEnv("ENABLE_THINKING", false)
	if err != nil {
		return Config{}, err
	}
	thinkingBudgetTokens, err := positiveIntEnv("THINKING_BUDGET_TOKENS", 2048)
	if err != nil {
		return Config{}, err
	}
	if err := validateThinkingBudgetTokens(thinkingBudgetTokens); err != nil {
		return Config{}, err
	}
	llmTokenBudget, err := positiveInt64Env("LLM_TOKEN_BUDGET", 32_768)
	if err != nil {
		return Config{}, err
	}
	llmMaxOutputTokens, err := positiveInt64Env("LLM_MAX_OUTPUT_TOKENS", 4_096)
	if err != nil {
		return Config{}, err
	}
	if llmMaxOutputTokens >= llmTokenBudget {
		return Config{}, fmt.Errorf("LLM_MAX_OUTPUT_TOKENS must be smaller than LLM_TOKEN_BUDGET")
	}
	llmRateTokensPerMinute, err := nonNegativeInt64Env("LLM_RATE_TOKENS_PER_MINUTE", 120_000)
	if err != nil {
		return Config{}, err
	}
	llmRateBurst, err := nonNegativeInt64Env("LLM_RATE_BURST", 120_000)
	if err != nil {
		return Config{}, err
	}
	llmIdempotencyTTL, err := positiveDurationEnv("LLM_IDEMPOTENCY_TTL", "24h")
	if err != nil {
		return Config{}, err
	}
	llmSyncMaxRetries, err := nonNegativeIntEnv("LLM_SYNC_MAX_RETRIES", 1)
	if err != nil {
		return Config{}, err
	}
	if llmSyncMaxRetries > 3 {
		return Config{}, fmt.Errorf("LLM_SYNC_MAX_RETRIES must not exceed 3")
	}
	llmRetryBackoff, err := positiveDurationEnv("LLM_RETRY_BACKOFF", "200ms")
	if err != nil {
		return Config{}, err
	}
	contextSummaryEnabled, err := boolEnv("CONTEXT_SUMMARY_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	contextSummaryMaxTokens, err := positiveInt64Env("CONTEXT_SUMMARY_MAX_TOKENS", 1024)
	if err != nil {
		return Config{}, err
	}
	contextKeepRecentMessages, err := positiveIntEnv("CONTEXT_KEEP_RECENT_MESSAGES", 8)
	if err != nil {
		return Config{}, err
	}
	contextSummaryMaxCalls, err := positiveIntEnv("CONTEXT_SUMMARY_MAX_CALLS", 2)
	if err != nil {
		return Config{}, err
	}
	if contextSummaryMaxCalls > 5 {
		return Config{}, fmt.Errorf("CONTEXT_SUMMARY_MAX_CALLS must not exceed 5")
	}
	contextQualityEnabled, err := boolEnv("CONTEXT_SUMMARY_QUALITY_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	contextQualityThreshold, err := positiveIntEnv("CONTEXT_SUMMARY_QUALITY_THRESHOLD", 70)
	if err != nil {
		return Config{}, err
	}
	if contextQualityThreshold > 100 {
		return Config{}, fmt.Errorf("CONTEXT_SUMMARY_QUALITY_THRESHOLD must not exceed 100")
	}
	contextQualityMaxTokens, err := positiveInt64Env("CONTEXT_SUMMARY_QUALITY_MAX_TOKENS", 256)
	if err != nil {
		return Config{}, err
	}
	titleGenerationMaxTokens, err := positiveInt64Env("TITLE_GENERATION_MAX_TOKENS", 64)
	if err != nil {
		return Config{}, err
	}
	titleGenerationMaxLength, err := positiveIntEnv("TITLE_GENERATION_MAX_LENGTH", 40)
	if err != nil {
		return Config{}, err
	}
	if titleGenerationMaxLength > 120 {
		return Config{}, fmt.Errorf("TITLE_GENERATION_MAX_LENGTH must not exceed 120")
	}
	titleGenerationTimeout, err := positiveDurationEnv("TITLE_GENERATION_TIMEOUT", "15s")
	if err != nil {
		return Config{}, err
	}
	objectStorageUseSSL, err := boolEnv("OBJECT_STORAGE_USE_SSL", false)
	if err != nil {
		return Config{}, err
	}
	attachmentMaxBytes, err := positiveInt64Env("ATTACHMENT_MAX_BYTES", 10<<20)
	if err != nil {
		return Config{}, err
	}
	attachmentMaxCount, err := positiveIntEnv("ATTACHMENT_MAX_COUNT", 5)
	if err != nil {
		return Config{}, err
	}
	attachmentMaxTextChars, err := positiveIntEnv("ATTACHMENT_MAX_TEXT_CHARS", 200_000)
	if err != nil {
		return Config{}, err
	}
	knowledge, err := loadKnowledgeConfig()
	if err != nil {
		return Config{}, err
	}
	rerank, err := loadRerankConfig()
	if err != nil {
		return Config{}, err
	}
	queryRewrite, err := loadQueryRewriteConfig()
	if err != nil {
		return Config{}, err
	}
	quotaEnabled, err := boolEnv("QUOTA_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	quotaDefaultDailyTokens, err := positiveInt64Env("QUOTA_DEFAULT_DAILY_TOKENS", 200_000)
	if err != nil {
		return Config{}, err
	}
	quotaDefaultMonthlyTokens, err := positiveInt64Env("QUOTA_DEFAULT_MONTHLY_TOKENS", 3_000_000)
	if err != nil {
		return Config{}, err
	}
	quotaMaxConcurrent, err := positiveIntEnv("QUOTA_MAX_CONCURRENT_GENERATIONS", 2)
	if err != nil {
		return Config{}, err
	}
	chatRateLimit, err := nonNegativeIntEnv("CHAT_RATE_LIMIT", 30)
	if err != nil {
		return Config{}, err
	}
	chatRateLimitWindow, err := positiveDurationEnv("CHAT_RATE_LIMIT_WINDOW", "1m")
	if err != nil {
		return Config{}, err
	}
	chatBodyMaxBytes, err := positiveInt64Env("CHAT_BODY_MAX_BYTES", 2<<20)
	if err != nil {
		return Config{}, err
	}
	messageMaxChars, err := positiveIntEnv("MESSAGE_MAX_CHARS", 100_000)
	if err != nil {
		return Config{}, err
	}
	toolArgumentMaxBytes, err := positiveIntEnv("TOOL_ARGUMENT_MAX_BYTES", 64<<10)
	if err != nil {
		return Config{}, err
	}
	toolResultMaxBytes, err := positiveIntEnv("TOOL_RESULT_MAX_BYTES", 32<<10)
	if err != nil {
		return Config{}, err
	}
	toolExecutionTimeout, err := positiveDurationEnv("TOOL_EXECUTION_TIMEOUT", "30s")
	if err != nil {
		return Config{}, err
	}
	moderationEnabled, err := boolEnv("MODERATION_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	alertCostThresholdNanoUSD, err := nonNegativeInt64Env("ALERT_COST_THRESHOLD_NANO_USD", 100_000_000)
	if err != nil {
		return Config{}, err
	}
	embedding, err := loadEmbeddingConfig()
	if err != nil {
		return Config{}, err
	}
	if knowledge.EmbeddingBatch > embedding.MaxInputs {
		return Config{}, fmt.Errorf("RAG_EMBEDDING_JOB_BATCH_SIZE must not exceed EMBEDDING_MAX_INPUTS")
	}

	providers, err := configuredProviders(streamConnectTimeout, llmTokenBudget, llmMaxOutputTokens)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:                    env("HTTP_ADDR", ":8080"),
		DatabaseURL:                 env("DATABASE_URL", "postgres://agent:agent_dev_password@localhost:5432/agent_platform?sslmode=disable"),
		DatabaseMaxOpenConns:        databaseMaxOpenConns,
		DatabaseMaxIdleConns:        databaseMaxIdleConns,
		DatabaseConnMaxLifetime:     databaseConnMaxLifetime,
		DatabaseConnMaxIdleTime:     databaseConnMaxIdleTime,
		RedisURL:                    env("REDIS_URL", "redis://localhost:6379/0"),
		ObjectStorageEndpoint:       env("OBJECT_STORAGE_ENDPOINT", "localhost:9000"),
		ObjectStorageAccessKey:      env("OBJECT_STORAGE_ACCESS_KEY", "agent_minio"),
		ObjectStorageSecretKey:      env("OBJECT_STORAGE_SECRET_KEY", "agent_minio_password"),
		ObjectStorageBucket:         env("OBJECT_STORAGE_BUCKET", "chat-attachments"),
		ObjectStorageRegion:         env("OBJECT_STORAGE_REGION", "us-east-1"),
		ObjectStorageUseSSL:         objectStorageUseSSL,
		AttachmentMaxBytes:          attachmentMaxBytes,
		AttachmentMaxCount:          attachmentMaxCount,
		AttachmentMaxTextChars:      attachmentMaxTextChars,
		JWTSecret:                   os.Getenv("JWT_SECRET"),
		AccessTokenTTL:              accessTokenTTL,
		RefreshTokenTTL:             refreshTokenTTL,
		RefreshCookieName:           env("REFRESH_COOKIE_NAME", "agent_refresh"),
		RefreshCookieSecure:         refreshCookieSecure,
		EmailVerificationTTL:        emailVerificationTTL,
		PasswordResetTTL:            passwordResetTTL,
		PublicWebURL:                strings.TrimRight(env("PUBLIC_WEB_URL", "http://localhost:5173"), "/"),
		EmailDeliveryMode:           strings.ToLower(env("EMAIL_DELIVERY_MODE", "log")),
		SMTPHost:                    strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:                    smtpPort,
		SMTPUsername:                strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		SMTPPassword:                os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:                    strings.TrimSpace(os.Getenv("SMTP_FROM")),
		AuthRateLimitWindow:         authRateLimitWindow,
		AuthLoginRateLimit:          authLoginRateLimit,
		AuthRegisterRateLimit:       authRegisterRateLimit,
		AuthPasswordResetRateLimit:  authPasswordResetRateLimit,
		AuthCredentialRetentionDays: authCredentialRetentionDays,
		AccountAuditRetentionDays:   accountAuditRetentionDays,
		CORSAllowedOrigins:          splitCSV(env("CORS_ALLOWED_ORIGINS", "http://localhost:5173")),
		LogLevel:                    env("LOG_LEVEL", "INFO"),
		LogDir:                      env("LOG_DIR", "./logs"),
		LogRetentionDays:            logRetentionDays,
		MaxToolIterations:           maxToolIterations,
		StreamConnectTimeout:        streamConnectTimeout,
		StreamTTFTTimeout:           streamTTFTTimeout,
		StreamTotalTimeout:          streamTotalTimeout,
		StreamCacheTTL:              streamCacheTTL,
		StreamCacheMaxEvents:        streamCacheMaxEvents,
		UserEventTTL:                userEventTTL,
		UserEventMaxEvents:          userEventMaxEvents,
		EnableThinking:              enableThinking,
		ThinkingBudgetTokens:        thinkingBudgetTokens,
		LLMTokenBudget:              llmTokenBudget,
		LLMMaxOutputTokens:          llmMaxOutputTokens,
		LLMRateTokensPerMinute:      llmRateTokensPerMinute,
		LLMRateBurst:                llmRateBurst,
		LLMIdempotencyTTL:           llmIdempotencyTTL,
		LLMSyncMaxRetries:           llmSyncMaxRetries,
		LLMRetryBackoff:             llmRetryBackoff,
		ContextSummaryEnabled:       contextSummaryEnabled,
		ContextSummaryProvider:      strings.TrimSpace(os.Getenv("CONTEXT_SUMMARY_PROVIDER")),
		ContextSummaryModel:         strings.TrimSpace(os.Getenv("CONTEXT_SUMMARY_MODEL")),
		ContextSummaryMaxTokens:     contextSummaryMaxTokens,
		ContextKeepRecentMessages:   contextKeepRecentMessages,
		ContextSummaryMaxCalls:      contextSummaryMaxCalls,
		ContextQualityEnabled:       contextQualityEnabled,
		ContextQualityThreshold:     contextQualityThreshold,
		ContextQualityMaxTokens:     contextQualityMaxTokens,
		TitleGenerationProvider:     strings.TrimSpace(os.Getenv("TITLE_GENERATION_PROVIDER")),
		TitleGenerationModel:        strings.TrimSpace(os.Getenv("TITLE_GENERATION_MODEL")),
		TitleGenerationMaxTokens:    titleGenerationMaxTokens,
		TitleGenerationMaxLength:    titleGenerationMaxLength,
		TitleGenerationTimeout:      titleGenerationTimeout,
		SystemPrompt:                env("SYSTEM_PROMPT", "You are a helpful AI assistant."),
		QuotaEnabled:                quotaEnabled,
		QuotaDefaultDailyTokens:     quotaDefaultDailyTokens,
		QuotaDefaultMonthlyTokens:   quotaDefaultMonthlyTokens,
		QuotaMaxConcurrent:          quotaMaxConcurrent,
		ChatRateLimit:               chatRateLimit,
		ChatRateLimitWindow:         chatRateLimitWindow,
		ChatBodyMaxBytes:            chatBodyMaxBytes,
		MessageMaxChars:             messageMaxChars,
		ToolArgumentMaxBytes:        toolArgumentMaxBytes,
		ToolResultMaxBytes:          toolResultMaxBytes,
		ToolExecutionTimeout:        toolExecutionTimeout,
		ToolAdminOnly:               splitCSV(os.Getenv("TOOL_ADMIN_ONLY")),
		ModerationEnabled:           moderationEnabled,
		ModerationInputBlocklist:    splitCSV(os.Getenv("MODERATION_INPUT_BLOCKLIST")),
		ModerationOutputBlocklist:   splitCSV(os.Getenv("MODERATION_OUTPUT_BLOCKLIST")),
		AdminEmails:                 splitCSV(os.Getenv("ADMIN_EMAILS")),
		AlertCostThresholdNanoUSD:   alertCostThresholdNanoUSD,
		Embedding:                   embedding,
		Knowledge:                   knowledge,
		Rerank:                      rerank,
		QueryRewrite:                queryRewrite,
		Providers:                   providers,
	}
	if len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET must contain at least 32 characters")
	}
	if cfg.RefreshTokenTTL <= cfg.AccessTokenTTL {
		return Config{}, fmt.Errorf("REFRESH_TOKEN_TTL must be longer than ACCESS_TOKEN_TTL")
	}
	switch cfg.EmailDeliveryMode {
	case "log", "disabled":
	case "smtp":
		if cfg.SMTPHost == "" || cfg.SMTPFrom == "" {
			return Config{}, fmt.Errorf("SMTP_HOST and SMTP_FROM are required when EMAIL_DELIVERY_MODE=smtp")
		}
	default:
		return Config{}, fmt.Errorf("EMAIL_DELIVERY_MODE must be log, smtp or disabled")
	}

	configured := make([]ProviderConfig, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			configured = append(configured, p)
		}
	}
	cfg.Providers = configured
	if err := validateSummaryTarget(cfg); err != nil {
		return Config{}, err
	}
	if err := validateOptionalModelTarget("TITLE_GENERATION", cfg.TitleGenerationProvider, cfg.TitleGenerationModel, cfg.Providers); err != nil {
		return Config{}, err
	}
	if err := validateOptionalModelTarget("RAG_QUERY_REWRITE", cfg.QueryRewrite.Provider, cfg.QueryRewrite.Model, cfg.Providers); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadKnowledgeConfig() (KnowledgeConfig, error) {
	maxBytes, err := positiveInt64Env("RAG_DOCUMENT_MAX_BYTES", 20<<20)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	maxTextChars, err := positiveIntEnv("RAG_DOCUMENT_MAX_TEXT_CHARS", 1_000_000)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	target, err := positiveInt64Env("RAG_CHUNK_TARGET_TOKENS", 500)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	overlap, err := nonNegativeInt64Env("RAG_CHUNK_OVERLAP_TOKENS", 80)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	minimum, err := positiveInt64Env("RAG_CHUNK_MIN_TOKENS", 40)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	if overlap >= target {
		return KnowledgeConfig{}, fmt.Errorf("RAG_CHUNK_OVERLAP_TOKENS must be smaller than RAG_CHUNK_TARGET_TOKENS")
	}
	if minimum > target {
		return KnowledgeConfig{}, fmt.Errorf("RAG_CHUNK_MIN_TOKENS must not exceed RAG_CHUNK_TARGET_TOKENS")
	}
	embeddingBatch, err := positiveIntEnv("RAG_EMBEDDING_JOB_BATCH_SIZE", 256)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	ingestionTimeout, err := positiveDurationEnv("RAG_INGESTION_TIMEOUT", "10m")
	if err != nil {
		return KnowledgeConfig{}, err
	}
	candidateLimit, err := positiveIntEnv("RAG_RETRIEVAL_CANDIDATE_LIMIT", 30)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	if candidateLimit > 200 {
		return KnowledgeConfig{}, fmt.Errorf("RAG_RETRIEVAL_CANDIDATE_LIMIT must not exceed 200")
	}
	topK, err := positiveIntEnv("RAG_RETRIEVAL_TOP_K", 5)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	if topK > candidateLimit {
		return KnowledgeConfig{}, fmt.Errorf("RAG_RETRIEVAL_TOP_K must not exceed RAG_RETRIEVAL_CANDIDATE_LIMIT")
	}
	similarityThreshold, err := floatEnv("RAG_RETRIEVAL_SIMILARITY_THRESHOLD", 0.35)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	if math.IsNaN(similarityThreshold) || math.IsInf(similarityThreshold, 0) || similarityThreshold < 0 || similarityThreshold > 1 {
		return KnowledgeConfig{}, fmt.Errorf("RAG_RETRIEVAL_SIMILARITY_THRESHOLD must be between 0 and 1")
	}
	contextTokenBudget, err := positiveInt64Env("RAG_RETRIEVAL_CONTEXT_TOKEN_BUDGET", 3000)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	maxKnowledgeBases, err := positiveIntEnv("RAG_RETRIEVAL_MAX_KNOWLEDGE_BASES", 8)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	maxQueryChars, err := positiveIntEnv("RAG_RETRIEVAL_MAX_QUERY_CHARS", 4000)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	traceEnabled, err := boolEnv("RAG_RETRIEVAL_TRACE_ENABLED", false)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	mode := strings.TrimSpace(env("RAG_RETRIEVAL_MODE", "exact_vector"))
	if mode != "exact_vector" && mode != "hybrid_rrf" {
		return KnowledgeConfig{}, fmt.Errorf("RAG_RETRIEVAL_MODE must be exact_vector or hybrid_rrf")
	}
	rrfK, err := positiveIntEnv("RAG_RETRIEVAL_RRF_K", 60)
	if err != nil {
		return KnowledgeConfig{}, err
	}
	if rrfK > 10000 {
		return KnowledgeConfig{}, fmt.Errorf("RAG_RETRIEVAL_RRF_K must not exceed 10000")
	}
	return KnowledgeConfig{
		DocumentMaxBytes: maxBytes, MaxTextChars: maxTextChars,
		ChunkTargetTokens: target, ChunkOverlap: overlap, ChunkMinTokens: minimum,
		EmbeddingBatch: embeddingBatch, IngestionTimeout: ingestionTimeout,
		RetrievalCandidateLimit: candidateLimit, RetrievalTopK: topK,
		RetrievalSimilarityThreshold: similarityThreshold, RetrievalContextTokenBudget: contextTokenBudget,
		RetrievalMaxKnowledgeBases: maxKnowledgeBases, RetrievalMaxQueryChars: maxQueryChars,
		RetrievalTraceEnabled: traceEnabled,
		RetrievalMode:         mode, RetrievalRRFK: rrfK,
	}, nil
}

func floatEnv(key string, fallback float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func loadEmbeddingConfig() (EmbeddingConfig, error) {
	enabled, err := boolEnv("EMBEDDING_ENABLED", false)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	dimension, err := positiveIntEnv("EMBEDDING_DIMENSION", 1536)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	sendDimensions, err := boolEnv("EMBEDDING_SEND_DIMENSIONS", true)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	maxInputs, err := positiveIntEnv("EMBEDDING_MAX_INPUTS", 2048)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	batchSize, err := positiveIntEnv("EMBEDDING_BATCH_SIZE", 64)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	if batchSize > maxInputs {
		return EmbeddingConfig{}, fmt.Errorf("EMBEDDING_BATCH_SIZE must not exceed EMBEDDING_MAX_INPUTS")
	}
	maxInputTokens, err := positiveInt64Env("EMBEDDING_MAX_INPUT_TOKENS", 8191)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	maxBatchTokens, err := positiveInt64Env("EMBEDDING_MAX_BATCH_TOKENS", 100_000)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	if maxBatchTokens < maxInputTokens {
		return EmbeddingConfig{}, fmt.Errorf("EMBEDDING_MAX_BATCH_TOKENS must be at least EMBEDDING_MAX_INPUT_TOKENS")
	}
	connectTimeout, err := positiveDurationEnv("EMBEDDING_CONNECT_TIMEOUT", "10s")
	if err != nil {
		return EmbeddingConfig{}, err
	}
	requestTimeout, err := positiveDurationEnv("EMBEDDING_REQUEST_TIMEOUT", "30s")
	if err != nil {
		return EmbeddingConfig{}, err
	}
	maxRetries, err := nonNegativeIntEnv("EMBEDDING_MAX_RETRIES", 2)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	if maxRetries > 3 {
		return EmbeddingConfig{}, fmt.Errorf("EMBEDDING_MAX_RETRIES must not exceed 3")
	}
	retryBackoff, err := positiveDurationEnv("EMBEDDING_RETRY_BACKOFF", "300ms")
	if err != nil {
		return EmbeddingConfig{}, err
	}
	cacheTTL, err := positiveDurationEnv("EMBEDDING_CACHE_TTL", "720h")
	if err != nil {
		return EmbeddingConfig{}, err
	}
	price, err := optionalNonNegativeFloatEnv("EMBEDDING_INPUT_USD_PER_MILLION_TOKENS")
	if err != nil {
		return EmbeddingConfig{}, err
	}
	tokenizerName := strings.TrimSpace(env("EMBEDDING_TOKENIZER", "cl100k_base"))
	if !supportedEmbeddingTokenizer(tokenizerName) {
		return EmbeddingConfig{}, fmt.Errorf("EMBEDDING_TOKENIZER %q is not supported", tokenizerName)
	}
	config := EmbeddingConfig{
		Enabled: enabled, Provider: strings.TrimSpace(env("EMBEDDING_PROVIDER", "openai")),
		BaseURL: strings.TrimRight(env("EMBEDDING_BASE_URL", "https://api.openai.com/v1"), "/"),
		APIKey:  os.Getenv("EMBEDDING_API_KEY"), Model: strings.TrimSpace(env("EMBEDDING_MODEL", "text-embedding-3-small")),
		Dimension: dimension, SendDimensions: sendDimensions, Tokenizer: tokenizerName,
		MaxInputs: maxInputs, BatchSize: batchSize, MaxInputTokens: maxInputTokens, MaxBatchTokens: maxBatchTokens,
		ConnectTimeout: connectTimeout, RequestTimeout: requestTimeout, MaxRetries: maxRetries,
		RetryBackoff: retryBackoff, CacheTTL: cacheTTL, InputUSDPerMillionTokens: price,
	}
	if config.Enabled && strings.TrimSpace(config.APIKey) == "" {
		return EmbeddingConfig{}, fmt.Errorf("EMBEDDING_API_KEY is required when EMBEDDING_ENABLED=true")
	}
	return config, nil
}

func validateSummaryTarget(cfg Config) error {
	providerID := strings.TrimSpace(cfg.ContextSummaryProvider)
	modelName := strings.TrimSpace(cfg.ContextSummaryModel)
	if (providerID == "") != (modelName == "") {
		return fmt.Errorf("CONTEXT_SUMMARY_PROVIDER and CONTEXT_SUMMARY_MODEL must be configured together")
	}
	if providerID == "" || !cfg.ContextSummaryEnabled {
		return nil
	}
	for _, candidate := range cfg.Providers {
		if candidate.ID != providerID {
			continue
		}
		for _, model := range candidate.Models {
			if model == modelName {
				return nil
			}
		}
		return fmt.Errorf("CONTEXT_SUMMARY_MODEL %q is not enabled for provider %q", modelName, providerID)
	}
	return fmt.Errorf("CONTEXT_SUMMARY_PROVIDER %q is not configured with an API key", providerID)
}

func validateOptionalModelTarget(prefix, providerID, modelName string, providers []ProviderConfig) error {
	providerID = strings.TrimSpace(providerID)
	modelName = strings.TrimSpace(modelName)
	if (providerID == "") != (modelName == "") {
		return fmt.Errorf("%s_PROVIDER and %s_MODEL must be configured together", prefix, prefix)
	}
	if providerID == "" {
		return nil
	}
	for _, candidate := range providers {
		if candidate.ID != providerID {
			continue
		}
		for _, model := range candidate.Models {
			if model == modelName {
				return nil
			}
		}
		return fmt.Errorf("%s_MODEL %q is not enabled for provider %q", prefix, modelName, providerID)
	}
	return fmt.Errorf("%s_PROVIDER %q is not configured with an API key", prefix, providerID)
}

func configuredProviders(connectTimeout time.Duration, defaultContextWindow, defaultMaxOutputTokens int64) ([]ProviderConfig, error) {
	type providerDefinition struct {
		id, label, kind, prefix, baseURL, models string
	}
	definitions := []providerDefinition{
		{"openai", "OpenAI", "openai-compatible", "OPENAI", "https://api.openai.com/v1", "gpt-4o-mini"},
		{"qwen", "通义千问", "openai-compatible", "QWEN", "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen3-vl-flash"},
		{"kimi", "Kimi", "openai-compatible", "KIMI", "https://api.moonshot.cn/v1", "moonshot-v1-8k"},
		{"anthropic", "Anthropic", "anthropic", "ANTHROPIC", "https://api.anthropic.com/v1", "claude-sonnet-4-20250514"},
		{"deepseek", "DeepSeek", "openai-compatible", "DEEPSEEK", "https://api.deepseek.com/v1", "deepseek-chat"},
		{"glm", "GLM", "openai-compatible", "GLM", "https://open.bigmodel.cn/api/paas/v4", "glm-4-flash"},
	}
	result := make([]ProviderConfig, 0, len(definitions))
	for _, definition := range definitions {
		pricing, err := parseModelPricing(env(definition.prefix+"_MODEL_PRICING", ""))
		if err != nil {
			return nil, fmt.Errorf("parse %s_MODEL_PRICING: %w", definition.prefix, err)
		}
		includeUsage, err := boolEnv(definition.prefix+"_STREAM_INCLUDE_USAGE", true)
		if err != nil {
			return nil, err
		}
		models := splitCSV(env(definition.prefix+"_MODELS", definition.models))
		visionModels := make(map[string]bool)
		for _, model := range splitCSV(env(definition.prefix+"_VISION_MODELS", "")) {
			enabled := false
			for _, candidate := range models {
				if candidate == model {
					enabled = true
					break
				}
			}
			if !enabled {
				return nil, fmt.Errorf("%s_VISION_MODELS model %q is not present in MODELS", definition.prefix, model)
			}
			visionModels[model] = true
		}
		limits, err := parseModelLimits(env(definition.prefix+"_MODEL_LIMITS", ""), models, ModelLimits{
			ContextWindow: defaultContextWindow, MaxOutputTokens: defaultMaxOutputTokens,
		})
		if err != nil {
			return nil, fmt.Errorf("parse %s_MODEL_LIMITS: %w", definition.prefix, err)
		}
		tokenizerDefaults := make(map[string]string, len(models))
		for _, model := range models {
			tokenizerDefaults[model] = "heuristic"
			if definition.id == "openai" {
				tokenizerDefaults[model] = defaultOpenAITokenizer(model)
			} else if definition.id == "deepseek" && strings.HasPrefix(model, "deepseek-v4-") {
				tokenizerDefaults[model] = "deepseek_v4"
			}
		}
		tokenizers, err := parseModelTokenizers(env(definition.prefix+"_MODEL_TOKENIZERS", ""), models, tokenizerDefaults)
		if err != nil {
			return nil, fmt.Errorf("parse %s_MODEL_TOKENIZERS: %w", definition.prefix, err)
		}
		result = append(result, ProviderConfig{
			ID:                 definition.id,
			Label:              definition.label,
			Type:               definition.kind,
			BaseURL:            strings.TrimRight(env(definition.prefix+"_BASE_URL", definition.baseURL), "/"),
			APIKey:             os.Getenv(definition.prefix + "_API_KEY"),
			Models:             models,
			StreamIncludeUsage: includeUsage,
			ModelPricing:       pricing,
			ModelLimits:        limits,
			ModelTokenizers:    tokenizers,
			VisionModels:       visionModels,
			ConnectTimeout:     connectTimeout,
		})
	}
	return result, nil
}

// parseModelTokenizers 解析逗号分隔的 model:tokenizer 配置。
// 支持 tiktoken 编码和项目内置的 DeepSeek V4 官方分词器，
// heuristic 表示使用保守的启发式估算作为回退。
func parseModelTokenizers(value string, models []string, defaults map[string]string) (map[string]string, error) {
	allowed := map[string]bool{"heuristic": true, "o200k_base": true, "cl100k_base": true, "p50k_base": true, "r50k_base": true, "deepseek_v4": true}
	result := make(map[string]string, len(models))
	for _, model := range models {
		tokenizerName := defaults[model]
		if tokenizerName == "" {
			tokenizerName = "heuristic"
		}
		if !allowed[tokenizerName] {
			return nil, fmt.Errorf("model %q uses unsupported default tokenizer %q", model, tokenizerName)
		}
		result[model] = tokenizerName
	}
	if strings.TrimSpace(value) == "" {
		return result, nil
	}
	for _, item := range splitCSV(value) {
		model, tokenizerName, ok := strings.Cut(item, ":")
		model = strings.TrimSpace(model)
		tokenizerName = strings.TrimSpace(tokenizerName)
		if !ok || model == "" || tokenizerName == "" {
			return nil, fmt.Errorf("%q must be model:tokenizer", item)
		}
		if _, enabled := result[model]; !enabled {
			return nil, fmt.Errorf("model %q is not present in the corresponding MODELS setting", model)
		}
		if !allowed[tokenizerName] {
			return nil, fmt.Errorf("model %q uses unsupported tokenizer %q", model, tokenizerName)
		}
		result[model] = tokenizerName
	}
	return result, nil
}

func defaultOpenAITokenizer(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"gpt-5", "gpt-4.5", "gpt-4.1", "gpt-4o", "o1", "o3", "o4"} {
		if strings.HasPrefix(model, prefix) {
			return "o200k_base"
		}
	}
	for _, prefix := range []string{"gpt-4", "gpt-3.5"} {
		if strings.HasPrefix(model, prefix) {
			return "cl100k_base"
		}
	}
	return "heuristic"
}

// parseModelLimits 解析逗号分隔的 model:context_window:max_output 配置。
// 未单独配置的已启用模型使用部署默认值，确保每个模型都有明确的预算。
func parseModelLimits(value string, models []string, defaults ModelLimits) (map[string]ModelLimits, error) {
	if defaults.ContextWindow <= 0 || defaults.MaxOutputTokens <= 0 || defaults.MaxOutputTokens >= defaults.ContextWindow {
		return nil, fmt.Errorf("default model limits are invalid")
	}
	limits := make(map[string]ModelLimits, len(models))
	for _, model := range models {
		limits[model] = defaults
	}
	if strings.TrimSpace(value) == "" {
		return limits, nil
	}
	for _, item := range splitCSV(value) {
		parts := strings.Split(item, ":")
		if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("%q must be model:context_window:max_output", item)
		}
		model := strings.TrimSpace(parts[0])
		if _, enabled := limits[model]; !enabled {
			return nil, fmt.Errorf("model %q is not present in the corresponding MODELS setting", model)
		}
		contextWindow, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || contextWindow <= 0 {
			return nil, fmt.Errorf("%q has an invalid context window", item)
		}
		maxOutput, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		if err != nil || maxOutput <= 0 || maxOutput >= contextWindow {
			return nil, fmt.Errorf("%q has an invalid max output", item)
		}
		limits[model] = ModelLimits{ContextWindow: contextWindow, MaxOutputTokens: maxOutput}
	}
	return limits, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func positiveIntEnv(key string, fallback int) (int, error) {
	value := env(key, fmt.Sprintf("%d", fallback))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func nonNegativeIntEnv(key string, fallback int) (int, error) {
	value := env(key, fmt.Sprintf("%d", fallback))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return parsed, nil
}

func positiveInt64Env(key string, fallback int64) (int64, error) {
	value := env(key, fmt.Sprintf("%d", fallback))
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func nonNegativeInt64Env(key string, fallback int64) (int64, error) {
	value := env(key, fmt.Sprintf("%d", fallback))
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return parsed, nil
}

func optionalNonNegativeFloatEnv(key string) (*float64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("%s must be a non-negative number", key)
	}
	return &parsed, nil
}

func supportedEmbeddingTokenizer(name string) bool {
	switch name {
	case "heuristic", "o200k_base", "cl100k_base", "p50k_base", "r50k_base":
		return true
	default:
		return false
	}
}

func positiveDurationEnv(key, fallback string) (time.Duration, error) {
	value := env(key, fallback)
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return parsed, nil
}

func validateThinkingBudgetTokens(value int) error {
	// Anthropic 的思考预算至少为 1024 Token，当前适配器使用 max_tokens=4096，
	// 因此将思考预算限制在此范围内，并为正文输出留出空间。
	if value < 1024 || value >= 4096 {
		return fmt.Errorf("THINKING_BUDGET_TOKENS must be between 1024 and 4095")
	}
	return nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := env(key, strconv.FormatBool(fallback))
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

// parseModelPricing 解析逗号分隔的价格配置，格式为
// model:input_usd_per_million:output_usd_per_million。
func parseModelPricing(value string) (map[string]ModelPricing, error) {
	pricing := make(map[string]ModelPricing)
	if strings.TrimSpace(value) == "" {
		return pricing, nil
	}
	for _, item := range splitCSV(value) {
		parts := strings.Split(item, ":")
		if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("%q must be model:input:output", item)
		}
		input, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil || input < 0 {
			return nil, fmt.Errorf("%q has an invalid input price", item)
		}
		output, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		if err != nil || output < 0 {
			return nil, fmt.Errorf("%q has an invalid output price", item)
		}
		pricing[strings.TrimSpace(parts[0])] = ModelPricing{InputUSDPerMillionTokens: input, OutputUSDPerMillionTokens: output}
	}
	return pricing, nil
}

func splitCSV(value string) []string {
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf("parse %s: expected KEY=VALUE", path)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set %s from %s: %w", key, path, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/mailer"
	"github.com/example/agent-platform/backend/internal/objectstore"
	"github.com/example/agent-platform/backend/internal/ratelimit"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/google/uuid"
)

type AuthConfig struct {
	RefreshTokenTTL        time.Duration
	EmailVerificationTTL   time.Duration
	PasswordResetTTL       time.Duration
	PublicWebURL           string
	RateLimitWindow        time.Duration
	LoginRateLimit         int
	RegisterRateLimit      int
	PasswordResetRateLimit int
}

type AuthService struct {
	repository repository.AuthRepository
	jwt        *auth.JWTManager
	mailer     mailer.Sender
	limiter    ratelimit.Limiter
	store      objectstore.Store
	config     AuthConfig
	now        func() time.Time
}

type ClientInfo struct {
	IP         string
	UserAgent  string
	DeviceName string
}

type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
	Client      ClientInfo
}

type LoginInput struct {
	Email    string
	Password string
	Client   ClientInfo
}

type AuthResult struct {
	User             model.User
	AccessToken      string
	ExpiresAt        time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	SessionID        string
}

type DeviceSession struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"deviceName"`
	Current    bool      `json:"current"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func NewAuth(repository repository.AuthRepository, jwt *auth.JWTManager, sender mailer.Sender, limiter ratelimit.Limiter, store objectstore.Store, config AuthConfig) *AuthService {
	if sender == nil {
		sender = mailer.NoopSender{}
	}
	if limiter == nil {
		limiter = ratelimit.Noop{}
	}
	if store == nil {
		store = objectstore.NewMemory()
	}
	return &AuthService{repository: repository, jwt: jwt, mailer: sender, limiter: limiter, store: store, config: config, now: time.Now}
}

func (s *AuthService) Register(ctx context.Context, input RegisterInput) (AuthResult, error) {
	input.Email = normalizeEmail(input.Email)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if err := s.enforceRateLimit(ctx, "register", input.Client.IP+"|"+input.Email, s.config.RegisterRateLimit); err != nil {
		return AuthResult{}, err
	}
	if !validEmail(input.Email) {
		return AuthResult{}, ErrInvalidEmail
	}
	if err := validatePassword(input.Password); err != nil {
		return AuthResult{}, err
	}
	if input.DisplayName == "" {
		input.DisplayName = strings.Split(input.Email, "@")[0]
	}
	if len([]rune(input.DisplayName)) > 80 {
		return AuthResult{}, ErrDisplayNameTooLong
	}

	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		return AuthResult{}, err
	}
	user := model.User{ID: uuid.NewString(), Email: input.Email, PasswordHash: passwordHash, DisplayName: input.DisplayName}
	session, rawRefresh, err := s.buildSession(user.ID, input.Client)
	if err != nil {
		return AuthResult{}, err
	}
	if err := s.repository.CreateUserAndSession(ctx, &user, &session); err != nil {
		if errors.Is(err, domain.ErrAlreadyExists) {
			return AuthResult{}, ErrEmailAlreadyRegistered
		}
		return AuthResult{}, err
	}
	s.audit(ctx, user.ID, &session.ID, "account.registered", input.Client, nil)
	if err := s.issueEmailVerification(ctx, user); err != nil {
		logger.Error(ctx, "send registration verification email failed", logger.Err(err), logger.String("user_id", user.ID))
	}
	return s.authResult(user, session, rawRefresh)
}

func (s *AuthService) Login(ctx context.Context, input LoginInput) (AuthResult, error) {
	input.Email = normalizeEmail(input.Email)
	if err := s.enforceRateLimit(ctx, "login", input.Client.IP+"|"+input.Email, s.config.LoginRateLimit); err != nil {
		return AuthResult{}, err
	}
	user, err := s.repository.FindUserByEmail(ctx, input.Email)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && auth.VerifyPassword(user.PasswordHash, input.Password) != nil) {
		return AuthResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return AuthResult{}, err
	}
	session, rawRefresh, err := s.buildSession(user.ID, input.Client)
	if err != nil {
		return AuthResult{}, err
	}
	if err := s.repository.CreateSession(ctx, &session); err != nil {
		return AuthResult{}, err
	}
	s.audit(ctx, user.ID, &session.ID, "session.login", input.Client, nil)
	return s.authResult(user, session, rawRefresh)
}

func (s *AuthService) Refresh(ctx context.Context, rawRefresh string, client ClientInfo) (AuthResult, error) {
	if rawRefresh == "" {
		return AuthResult{}, ErrInvalidRefreshToken
	}
	nextRefresh, err := auth.NewOpaqueToken()
	if err != nil {
		return AuthResult{}, err
	}
	now := s.now()
	session, err := s.repository.RotateSession(ctx, auth.HashOpaqueToken(rawRefresh), auth.HashOpaqueToken(nextRefresh), now)
	if errors.Is(err, domain.ErrNotFound) {
		return AuthResult{}, ErrInvalidRefreshToken
	}
	if err != nil {
		return AuthResult{}, err
	}
	user, err := s.repository.FindUserByID(ctx, session.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return AuthResult{}, ErrInvalidRefreshToken
	}
	if err != nil {
		return AuthResult{}, err
	}
	s.audit(ctx, user.ID, &session.ID, "session.refreshed", client, nil)
	return s.authResult(user, session, nextRefresh)
}

func (s *AuthService) Logout(ctx context.Context, rawRefresh string, client ClientInfo) error {
	if rawRefresh == "" {
		return nil
	}
	session, err := s.repository.RevokeSessionByTokenHash(ctx, auth.HashOpaqueToken(rawRefresh), "logout", s.now())
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	s.audit(ctx, session.UserID, &session.ID, "session.logout", client, nil)
	return nil
}

func (s *AuthService) LogoutAll(ctx context.Context, userID, sessionID string, client ClientInfo) error {
	if err := s.repository.RevokeAllSessions(ctx, userID, "logout_all", s.now()); err != nil {
		return err
	}
	s.audit(ctx, userID, optionalString(sessionID), "session.logout_all", client, nil)
	return nil
}

func (s *AuthService) Me(ctx context.Context, userID string) (model.User, error) {
	user, err := s.repository.FindUserByID(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return model.User{}, ErrUnauthenticated
	}
	return user, err
}

func (s *AuthService) SessionActive(ctx context.Context, userID, sessionID string) (bool, error) {
	return s.repository.IsSessionActive(ctx, userID, sessionID, s.now())
}

func (s *AuthService) Sessions(ctx context.Context, userID, currentSessionID string) ([]DeviceSession, error) {
	items, err := s.repository.ListSessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make([]DeviceSession, 0, len(items))
	for _, item := range items {
		result = append(result, DeviceSession{
			ID: item.ID, DeviceName: item.DeviceName, Current: item.ID == currentSessionID,
			CreatedAt: item.CreatedAt, LastUsedAt: item.LastUsedAt, ExpiresAt: item.ExpiresAt,
		})
	}
	return result, nil
}

func (s *AuthService) RevokeSession(ctx context.Context, userID, currentSessionID, targetSessionID string, client ClientInfo) error {
	if err := s.repository.RevokeSession(ctx, userID, targetSessionID, "device_logout", s.now()); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrSessionNotFound
		}
		return err
	}
	s.audit(ctx, userID, optionalString(currentSessionID), "session.revoked", client, map[string]any{"targetSessionId": targetSessionID})
	return nil
}

func (s *AuthService) VerifyEmail(ctx context.Context, rawToken string, client ClientInfo) (model.User, error) {
	if rawToken == "" {
		return model.User{}, ErrInvalidAccountToken
	}
	user, err := s.repository.VerifyEmailWithToken(ctx, auth.HashOpaqueToken(rawToken), s.now())
	if errors.Is(err, domain.ErrNotFound) {
		return model.User{}, ErrInvalidAccountToken
	}
	if err != nil {
		return model.User{}, err
	}
	s.audit(ctx, user.ID, nil, "email.verified", client, nil)
	return user, nil
}

func (s *AuthService) ResendVerification(ctx context.Context, userID string, client ClientInfo) error {
	if err := s.enforceRateLimit(ctx, "verify", client.IP+"|"+userID, s.config.PasswordResetRateLimit); err != nil {
		return err
	}
	user, err := s.repository.FindUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.EmailVerifiedAt != nil {
		return nil
	}
	if err := s.issueEmailVerification(ctx, user); err != nil {
		return ErrEmailDeliveryUnavailable
	}
	s.audit(ctx, user.ID, nil, "email.verification_resent", client, nil)
	return nil
}

// ForgotPassword intentionally returns nil for unknown addresses and delivery
// failures so callers cannot use response differences to enumerate accounts.
func (s *AuthService) ForgotPassword(ctx context.Context, email string, client ClientInfo) error {
	email = normalizeEmail(email)
	if err := s.enforceRateLimit(ctx, "forgot", client.IP+"|"+email, s.config.PasswordResetRateLimit); err != nil {
		return err
	}
	user, err := s.repository.FindUserByEmail(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.issuePasswordReset(ctx, user); err != nil {
		logger.Error(ctx, "send password reset email failed", logger.Err(err), logger.String("user_id", user.ID))
		return nil
	}
	s.audit(ctx, user.ID, nil, "password.reset_requested", client, nil)
	return nil
}

func (s *AuthService) ResetPassword(ctx context.Context, rawToken, newPassword string, client ClientInfo) error {
	if err := s.enforceRateLimit(ctx, "reset", client.IP+"|"+auth.HashOpaqueToken(rawToken), s.config.PasswordResetRateLimit); err != nil {
		return err
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	passwordHash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	userID, err := s.repository.ResetPasswordWithToken(ctx, auth.HashOpaqueToken(rawToken), passwordHash, "password_reset", s.now())
	if errors.Is(err, domain.ErrNotFound) {
		return ErrInvalidAccountToken
	}
	if err != nil {
		return err
	}
	s.audit(ctx, userID, nil, "password.reset", client, nil)
	return nil
}

func (s *AuthService) ChangePassword(ctx context.Context, userID, sessionID, currentPassword, newPassword string, client ClientInfo) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	user, err := s.repository.FindUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if auth.VerifyPassword(user.PasswordHash, currentPassword) != nil {
		return ErrInvalidCredentials
	}
	if currentPassword == newPassword {
		return ErrPasswordUnchanged
	}
	passwordHash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.repository.UpdatePasswordAndRevokeSessions(ctx, userID, passwordHash, "password_changed", s.now()); err != nil {
		return err
	}
	s.audit(ctx, userID, optionalString(sessionID), "password.changed", client, nil)
	return nil
}

func (s *AuthService) ExportAccount(ctx context.Context, userID, sessionID string, client ClientInfo) (repository.AccountExport, error) {
	result, err := s.repository.ExportAccount(ctx, userID, s.now())
	if err == nil {
		s.audit(ctx, userID, optionalString(sessionID), "account.exported", client, nil)
	}
	return result, err
}

func (s *AuthService) DeleteAccount(ctx context.Context, userID, sessionID, password, confirmation string, client ClientInfo) error {
	if confirmation != "DELETE" {
		return ErrAccountDeleteConfirmation
	}
	user, err := s.repository.FindUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if auth.VerifyPassword(user.PasswordHash, password) != nil {
		return ErrInvalidCredentials
	}
	keys, err := s.repository.ListObjectKeysByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := s.store.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete account object %q: %w", key, err)
		}
	}
	s.audit(ctx, userID, optionalString(sessionID), "account.deleted", client, map[string]any{"attachmentCount": len(keys)})
	return s.repository.DeleteAccount(ctx, userID)
}

func (s *AuthService) CleanupAuditLogs(ctx context.Context, before time.Time) error {
	return s.repository.CleanupAuditLogs(ctx, before)
}

func (s *AuthService) buildSession(userID string, client ClientInfo) (model.AuthSession, string, error) {
	raw, err := auth.NewOpaqueToken()
	if err != nil {
		return model.AuthSession{}, "", err
	}
	now := s.now()
	session := model.AuthSession{
		ID: uuid.NewString(), UserID: userID, RefreshTokenHash: auth.HashOpaqueToken(raw),
		DeviceName: deviceName(client), UserAgent: truncate(client.UserAgent, 500),
		ExpiresAt: now.Add(s.config.RefreshTokenTTL), LastUsedAt: now,
	}
	return session, raw, nil
}

func (s *AuthService) authResult(user model.User, session model.AuthSession, rawRefresh string) (AuthResult, error) {
	access, expiresAt, err := s.jwt.Issue(user.ID, user.Email, session.ID)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{
		User: user, AccessToken: access, ExpiresAt: expiresAt,
		RefreshToken: rawRefresh, RefreshExpiresAt: session.ExpiresAt, SessionID: session.ID,
	}, nil
}

func (s *AuthService) issueEmailVerification(ctx context.Context, user model.User) error {
	raw, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	token := model.AccountToken{
		UserID: user.ID, Purpose: model.AccountTokenEmailVerification,
		TokenHash: auth.HashOpaqueToken(raw), ExpiresAt: s.now().Add(s.config.EmailVerificationTTL),
	}
	if err := s.repository.CreateAccountToken(ctx, &token); err != nil {
		return err
	}
	link := strings.TrimRight(s.config.PublicWebURL, "/") + "/?verifyToken=" + url.QueryEscape(raw)
	return s.mailer.Send(ctx, mailer.Message{
		To: user.Email, Subject: "验证你的 Agent Workspace 邮箱",
		Text: "请打开下面的链接完成邮箱验证。链接将在 " + s.config.EmailVerificationTTL.String() + " 后失效：\n\n" + link,
	})
}

func (s *AuthService) issuePasswordReset(ctx context.Context, user model.User) error {
	raw, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	token := model.AccountToken{
		UserID: user.ID, Purpose: model.AccountTokenPasswordReset,
		TokenHash: auth.HashOpaqueToken(raw), ExpiresAt: s.now().Add(s.config.PasswordResetTTL),
	}
	if err := s.repository.CreateAccountToken(ctx, &token); err != nil {
		return err
	}
	link := strings.TrimRight(s.config.PublicWebURL, "/") + "/?resetToken=" + url.QueryEscape(raw)
	return s.mailer.Send(ctx, mailer.Message{
		To: user.Email, Subject: "重置你的 Agent Workspace 密码",
		Text: "请打开下面的链接设置新密码。链接只能使用一次，并将在 " + s.config.PasswordResetTTL.String() + " 后失效：\n\n" + link,
	})
}

func (s *AuthService) enforceRateLimit(ctx context.Context, action, identity string, limit int) error {
	if limit <= 0 {
		return nil
	}
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(identity))))
	decision, err := s.limiter.Allow(ctx, "auth:"+action+":"+hex.EncodeToString(digest[:]), limit, s.config.RateLimitWindow)
	if err != nil {
		logger.Error(ctx, "auth rate limiter failed open", logger.Err(err), logger.String("action", action))
		return nil
	}
	if !decision.Allowed {
		return &RateLimitError{RetryAfter: decision.RetryAfter}
	}
	return nil
}

func (s *AuthService) audit(ctx context.Context, userID string, sessionID *string, event string, client ClientInfo, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	payload, _ := json.Marshal(metadata)
	entry := model.AccountAuditLog{
		UserID: userID, SessionID: sessionID, Event: event,
		UserAgent: truncate(client.UserAgent, 500), Metadata: payload,
	}
	if err := s.repository.RecordAudit(ctx, &entry); err != nil {
		logger.Error(ctx, "account audit persistence failed", logger.Err(err), logger.String("event", event), logger.String("user_id", userID))
	}
}

func normalizeEmail(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func validEmail(value string) bool {
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}

func validatePassword(value string) error {
	if len(value) < 8 {
		return ErrPasswordTooShort
	}
	if len([]byte(value)) > 72 {
		return ErrPasswordTooLong
	}
	return nil
}

func deviceName(client ClientInfo) string {
	if value := strings.TrimSpace(client.DeviceName); value != "" {
		return truncate(value, 120)
	}
	if value := strings.TrimSpace(client.UserAgent); value != "" {
		return truncate(value, 120)
	}
	return "未知设备"
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

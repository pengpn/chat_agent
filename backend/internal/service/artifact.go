package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/userevent"
	"github.com/google/uuid"
)

type ArtifactService struct {
	access toolAccess
	repo   repository.AgentToolRepository
}

func NewArtifact(repo repository.AgentToolRepository, conversations repository.ConversationRepository, events *userevent.Hub) *ArtifactService {
	return &ArtifactService{toolAccess{conversations, events}, repo}
}
func (s *ArtifactService) Get(ctx context.Context, userID, conversationID, id string) (model.AgentArtifact, error) {
	if err := s.access.owned(ctx, userID, conversationID); err != nil {
		return model.AgentArtifact{}, err
	}
	return s.find(ctx, userID, conversationID, id)
}
func (s *ArtifactService) find(ctx context.Context, userID, conversationID, id string) (model.AgentArtifact, error) {
	if _, err := uuid.Parse(id); err != nil {
		return model.AgentArtifact{}, domain.ErrNotFound
	}
	return s.repo.GetArtifact(ctx, userID, conversationID, id)
}
func (s *ArtifactService) Save(ctx context.Context, input domain.SaveArtifactInput) (domain.SavedArtifact, error) {
	var result domain.SavedArtifact
	scope, err := s.access.scope(ctx)
	if err != nil {
		return result, err
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || len([]rune(input.Title)) > 200 || input.Content == "" || len(input.Content) > 32<<10 || !utf8.ValidString(input.Content) {
		return result, ErrInvalidToolInput
	}
	if input.MediaType != "text/plain" && input.MediaType != "text/markdown" && input.MediaType != "application/json" {
		return result, ErrInvalidToolInput
	}
	if input.MediaType == "application/json" && !json.Valid([]byte(input.Content)) {
		return result, fmt.Errorf("JSON 产物内容必须是有效 JSON")
	}
	hash := sha256.Sum256([]byte(input.Content))
	artifact := model.AgentArtifact{UserID: scope.UserID, ConversationID: scope.ConversationID, Title: input.Title, MediaType: input.MediaType, Content: input.Content, SHA256: hex.EncodeToString(hash[:]), Size: len(input.Content)}
	if err := s.repo.SaveArtifact(ctx, &artifact); err != nil {
		return result, err
	}
	s.access.changed(scope.UserID, scope.ConversationID)
	return domain.SavedArtifact{ArtifactID: artifact.ID, Title: artifact.Title, SHA256: artifact.SHA256, Size: artifact.Size}, nil
}
func (s *ArtifactService) Read(ctx context.Context, input domain.ReadArtifactInput) (domain.ArtifactPage, error) {
	var result domain.ArtifactPage
	scope, err := s.access.scope(ctx)
	if err != nil {
		return result, err
	}
	artifact, err := s.find(ctx, scope.UserID, scope.ConversationID, input.ArtifactID)
	if err != nil {
		return result, err
	}
	if input.Offset < 0 || input.Offset > len(artifact.Content) || (input.Offset < len(artifact.Content) && !utf8.RuneStart(artifact.Content[input.Offset])) || input.Limit < 0 || input.Limit > 4096 {
		return result, ErrInvalidToolInput
	}
	if input.Limit == 0 {
		input.Limit = 4096
	}
	end := min(input.Offset+input.Limit, len(artifact.Content))
	for end < len(artifact.Content) && end > input.Offset && !utf8.RuneStart(artifact.Content[end]) {
		end--
	}
	if end == input.Offset && end < len(artifact.Content) {
		return result, fmt.Errorf("limit 太小，无法容纳一个 UTF-8 字符")
	}
	return domain.ArtifactPage{ArtifactID: artifact.ID, Content: artifact.Content[input.Offset:end], SHA256: artifact.SHA256, NextOffset: end, HasMore: end < len(artifact.Content)}, nil
}

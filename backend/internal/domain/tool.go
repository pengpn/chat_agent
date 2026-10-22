package domain

import (
	"encoding/json"
	"time"
)

// ToolMetadata 是工具身份与能力说明，不包含执行器或数据库依赖。
type ToolMetadata struct {
	Namespace        string   `json:"namespace"`
	Name             string   `json:"name"`
	Version          string   `json:"version"`
	Category         string   `json:"category"`
	Source           string   `json:"source"`
	Exposure         string   `json:"exposure"`
	Capabilities     []string `json:"capabilities"`
	WireName         string   `json:"wireName"`
	RequiresApproval bool     `json:"requiresApproval"`
}

func (m ToolMetadata) ID() string { return m.Source + ":" + m.Namespace + "." + m.Name }

// ToolDescriptor 固定工具版本和 Schema 指纹，供 Registry 与审批业务共同使用。
type ToolDescriptor struct {
	ToolMetadata
	SchemaHash string `json:"schemaHash"`
}

type PlanStep struct {
	Title  string `json:"title"`
	Status string `json:"status"`
}
type UpdatePlanInput struct {
	Steps []PlanStep `json:"steps"`
}
type Plan struct {
	ConversationID string     `json:"conversationId"`
	Steps          []PlanStep `json:"steps"`
	Revision       int64      `json:"revision"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}
type AskUserInput struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}
type UserAnswer struct {
	Answer string `json:"answer"`
}
type ApprovalAnswer struct {
	Approved bool `json:"approved"`
}

// ApprovalRequest 的 Target 由工具适配器从本轮快照解析，不能由模型提交元数据。
type ApprovalRequest struct {
	Target    ToolDescriptor
	Arguments json.RawMessage
	Reason    string
}
type SaveArtifactInput struct {
	Title     string `json:"title"`
	MediaType string `json:"mediaType"`
	Content   string `json:"content"`
}
type SavedArtifact struct {
	ArtifactID string `json:"artifactId"`
	Title      string `json:"title"`
	SHA256     string `json:"sha256"`
	Size       int    `json:"size"`
}
type ReadArtifactInput struct {
	ArtifactID string `json:"artifactId"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
}
type ArtifactPage struct {
	ArtifactID string `json:"artifactId"`
	Content    string `json:"content"`
	SHA256     string `json:"sha256"`
	NextOffset int    `json:"nextOffset"`
	HasMore    bool   `json:"hasMore"`
}

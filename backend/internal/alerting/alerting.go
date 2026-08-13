// Package alerting 收集运营告警的统一出口。
// 告警与业务日志分离：业务日志记录“发生了什么”，
// 告警记录“需要人介入查看什么”。默认实现输出带 alert_kind
// 字段的 ERROR 级结构化日志，运维可以用日志管道按该字段配置通知。
package alerting

import (
	"context"

	"github.com/example/agent-platform/backend/internal/logger"
)

// Kind 是告警类别，用于告警路由和面板分组。
type Kind string

const (
	// KindCostAnomaly 表示单条消息成本超过阈值。
	KindCostAnomaly Kind = "cost_anomaly"
	// KindCallSurge 表示同一用户的请求频率触发限流，可能存在刷接口行为。
	KindCallSurge Kind = "call_surge"
	// KindModerationHit 表示输入或输出命中内容审核策略。
	KindModerationHit Kind = "moderation_hit"
	// KindQuotaExhausted 表示用户额度或并发上限被拒绝。
	KindQuotaExhausted Kind = "quota_exhausted"
)

// Alerter 是告警发送边界。实现不得阻塞业务请求，也不得返回错误影响主流程。
type Alerter interface {
	Alert(ctx context.Context, kind Kind, fields ...logger.Field)
}

// Noop 关闭告警时使用。
type Noop struct{}

func (Noop) Alert(context.Context, Kind, ...logger.Field) {}

// ZapAlerter 把告警写成 ERROR 级结构化日志。
// alert_kind 字段是日志管道配置告警规则的稳定锚点。
type ZapAlerter struct{}

func NewZap() ZapAlerter { return ZapAlerter{} }

func (ZapAlerter) Alert(ctx context.Context, kind Kind, fields ...logger.Field) {
	all := append([]logger.Field{logger.String("alert_kind", string(kind))}, fields...)
	logger.Error(ctx, "operational alert", all...)
}

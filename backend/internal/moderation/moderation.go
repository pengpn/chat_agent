// Package moderation 提供输入与输出内容审核的统一边界。
// 审核策略刻意做成可替换接口：当前实现是基于关键词的保守策略，
// 生产环境可以替换为第三方审核服务，而业务层不需要改动。
package moderation

import "strings"

// Direction 区分审核发生在用户输入侧还是模型输出侧。
// 两个方向允许配置不同的策略词表。
type Direction string

const (
	DirectionInput  Direction = "input"
	DirectionOutput Direction = "output"
)

// BlockedReply 是输出被拦截后展示给用户的固定文案。
// 模型原始内容不会被保存或展示。
const BlockedReply = "很抱歉，该内容可能包含敏感信息，无法展示。"

// Decision 是一次审核的结论。Blocked 为 true 时 Term 给出命中的策略词，
// 仅用于日志和告警，不应回传给终端用户。
type Decision struct {
	Blocked bool
	Term    string
}

// Moderator 实现一类内容审核策略。实现必须是无副作用且可并发调用的。
type Moderator interface {
	Check(direction Direction, content string) Decision
}

// Noop 表示未启用审核，永远放行。
type Noop struct{}

func (Noop) Check(Direction, string) Decision { return Decision{} }

// KeywordPolicy 对内容做大小写不敏感的关键词匹配。
// 它是最保守的一层防线，用于拦截明确的违规词表；
// 命中即拦截，不做语义判断。
type KeywordPolicy struct {
	inputTerms  []string
	outputTerms []string
}

// New 根据两个方向的词表构造审核器。enabled 为 false 或两个词表都为空时
// 返回 Noop，调用方不需要再做空值判断。
func New(enabled bool, inputTerms, outputTerms []string) Moderator {
	if !enabled {
		return Noop{}
	}
	inputTerms = normalizeTerms(inputTerms)
	outputTerms = normalizeTerms(outputTerms)
	if len(inputTerms) == 0 && len(outputTerms) == 0 {
		return Noop{}
	}
	return &KeywordPolicy{inputTerms: inputTerms, outputTerms: outputTerms}
}

func (p *KeywordPolicy) Check(direction Direction, content string) Decision {
	if content == "" {
		return Decision{}
	}
	lowered := strings.ToLower(content)
	terms := p.outputTerms
	if direction == DirectionInput {
		terms = p.inputTerms
	}
	for _, term := range terms {
		if strings.Contains(lowered, term) {
			return Decision{Blocked: true, Term: term}
		}
	}
	return Decision{}
}

func normalizeTerms(terms []string) []string {
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		if term = strings.ToLower(strings.TrimSpace(term)); term != "" {
			result = append(result, term)
		}
	}
	return result
}

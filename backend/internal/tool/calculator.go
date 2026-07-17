package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/example/agent-platform/backend/internal/provider"
)

type calculator struct{}

func NewCalculator() Tool { return calculator{} }

func (calculator) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{
		Type: "function",
		Function: provider.ToolFunctionDefinition{
			Name:        "calculator",
			Description: "Evaluate a basic arithmetic expression. Use it for calculations instead of mental arithmetic.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"expression":{"type":"string","description":"Arithmetic using numbers, +, -, *, /, and parentheses."}},"required":["expression"],"additionalProperties":false}`),
		},
	}
}

func (calculator) Execute(ctx context.Context, rawArguments string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(rawArguments) == 0 || len(rawArguments) > 4<<10 {
		return "", fmt.Errorf("calculator arguments must contain 1 to 4096 bytes")
	}
	var input struct {
		Expression string `json:"expression"`
	}
	decoder := json.NewDecoder(strings.NewReader(rawArguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", fmt.Errorf("calculator arguments must contain expression: %w", err)
	}
	input.Expression = strings.TrimSpace(input.Expression)
	if input.Expression == "" || len([]rune(input.Expression)) > 256 {
		return "", fmt.Errorf("calculator expression must contain 1 to 256 characters")
	}
	result, err := parseExpression(input.Expression)
	if err != nil {
		return "", err
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return "", fmt.Errorf("calculator result is not finite")
	}
	output, _ := json.Marshal(struct {
		Result float64 `json:"result"`
	}{Result: result})
	return string(output), nil
}

type expressionParser struct {
	input []rune
	pos   int
}

func parseExpression(input string) (float64, error) {
	parser := &expressionParser{input: []rune(input)}
	result, err := parser.expression()
	if err != nil {
		return 0, err
	}
	parser.skipSpaces()
	if parser.pos != len(parser.input) {
		return 0, fmt.Errorf("unexpected character %q", parser.input[parser.pos])
	}
	return result, nil
}

func (p *expressionParser) expression() (float64, error) {
	value, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpaces()
		if p.consume('+') {
			right, err := p.term()
			if err != nil {
				return 0, err
			}
			value += right
			continue
		}
		if p.consume('-') {
			right, err := p.term()
			if err != nil {
				return 0, err
			}
			value -= right
			continue
		}
		return value, nil
	}
}

func (p *expressionParser) term() (float64, error) {
	value, err := p.factor()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpaces()
		if p.consume('*') {
			right, err := p.factor()
			if err != nil {
				return 0, err
			}
			value *= right
			continue
		}
		if p.consume('/') {
			right, err := p.factor()
			if err != nil {
				return 0, err
			}
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			value /= right
			continue
		}
		return value, nil
	}
}

func (p *expressionParser) factor() (float64, error) {
	p.skipSpaces()
	if p.consume('+') {
		return p.factor()
	}
	if p.consume('-') {
		value, err := p.factor()
		return -value, err
	}
	if p.consume('(') {
		value, err := p.expression()
		if err != nil {
			return 0, err
		}
		p.skipSpaces()
		if !p.consume(')') {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		return value, nil
	}
	return p.number()
}

func (p *expressionParser) number() (float64, error) {
	p.skipSpaces()
	start := p.pos
	hasDigit := false
	for p.pos < len(p.input) && unicode.IsDigit(p.input[p.pos]) {
		hasDigit = true
		p.pos++
	}
	if p.pos < len(p.input) && p.input[p.pos] == '.' {
		p.pos++
		for p.pos < len(p.input) && unicode.IsDigit(p.input[p.pos]) {
			hasDigit = true
			p.pos++
		}
	}
	if !hasDigit {
		return 0, fmt.Errorf("expected a number")
	}
	if p.pos < len(p.input) && (p.input[p.pos] == 'e' || p.input[p.pos] == 'E') {
		exponentStart := p.pos
		p.pos++
		if p.pos < len(p.input) && (p.input[p.pos] == '+' || p.input[p.pos] == '-') {
			p.pos++
		}
		digitsStart := p.pos
		for p.pos < len(p.input) && unicode.IsDigit(p.input[p.pos]) {
			p.pos++
		}
		if digitsStart == p.pos {
			return 0, fmt.Errorf("invalid exponent near %q", string(p.input[exponentStart:p.pos]))
		}
	}
	value, err := strconv.ParseFloat(string(p.input[start:p.pos]), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %w", err)
	}
	return value, nil
}

func (p *expressionParser) skipSpaces() {
	for p.pos < len(p.input) && unicode.IsSpace(p.input[p.pos]) {
		p.pos++
	}
}

func (p *expressionParser) consume(expected rune) bool {
	if p.pos < len(p.input) && p.input[p.pos] == expected {
		p.pos++
		return true
	}
	return false
}

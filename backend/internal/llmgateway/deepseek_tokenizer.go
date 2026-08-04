package llmgateway

import (
	"container/heap"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/dlclark/regexp2"
	"github.com/example/agent-platform/backend/internal/provider"
)

const (
	deepSeekV4TokenizerName = "deepseek_v4"
	deepSeekV4BOS           = "<｜begin▁of▁sentence｜>"
	deepSeekV4EOS           = "<｜end▁of▁sentence｜>"
	deepSeekV4User          = "<｜User｜>"
	deepSeekV4Assistant     = "<｜Assistant｜>"
	deepSeekV4ThinkStart    = "<think>"
	deepSeekV4ThinkEnd      = "</think>"
	deepSeekV4DSML          = "｜DSML｜"
)

// deepSeekV4TokenizerJSON 来自 DeepSeek 官方 DeepSeek-V4-Flash 模型仓库。
// 固定在源码中可避免启动时访问外网，也避免上游词表变更导致估算结果漂移。
//
//go:embed tokenizer_assets/deepseek_v4_tokenizer.json
var deepSeekV4TokenizerJSON []byte

var (
	deepSeekV4Once      sync.Once
	deepSeekV4Tokenizer *deepSeekTokenizer
	deepSeekV4LoadErr   error
)

type deepSeekTokenizerConfig struct {
	AddedTokens []struct {
		Content string `json:"content"`
	} `json:"added_tokens"`
	Model struct {
		Vocab  map[string]int `json:"vocab"`
		Merges []string       `json:"merges"`
	} `json:"model"`
}

type deepSeekPair struct {
	left  string
	right string
}

type deepSeekTokenTrie struct {
	children map[rune]*deepSeekTokenTrie
	terminal bool
}

type deepSeekTokenizer struct {
	merges        map[deepSeekPair]int
	addedTokens   *deepSeekTokenTrie
	preTokenizers []*regexp2.Regexp
	byteEncoder   [256]rune
}

func loadDeepSeekV4Tokenizer() (*deepSeekTokenizer, error) {
	deepSeekV4Once.Do(func() {
		deepSeekV4Tokenizer, deepSeekV4LoadErr = newDeepSeekTokenizer(deepSeekV4TokenizerJSON)
	})
	return deepSeekV4Tokenizer, deepSeekV4LoadErr
}

func newDeepSeekTokenizer(data []byte) (*deepSeekTokenizer, error) {
	var cfg deepSeekTokenizerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 DeepSeek V4 tokenizer: %w", err)
	}
	if len(cfg.Model.Vocab) == 0 || len(cfg.Model.Merges) == 0 {
		return nil, fmt.Errorf("DeepSeek V4 tokenizer 缺少词表或合并规则")
	}

	merges := make(map[deepSeekPair]int, len(cfg.Model.Merges))
	for rank, merge := range cfg.Model.Merges {
		left, right, ok := strings.Cut(merge, " ")
		if !ok || left == "" || right == "" {
			return nil, fmt.Errorf("DeepSeek V4 tokenizer 合并规则 %q 无效", merge)
		}
		merges[deepSeekPair{left: left, right: right}] = rank
	}

	trie := &deepSeekTokenTrie{children: make(map[rune]*deepSeekTokenTrie)}
	for _, token := range cfg.AddedTokens {
		if token.Content != "" {
			trie.add(token.Content)
		}
	}

	patterns := []string{
		`\p{N}{1,3}`,
		`[\x{4E00}-\x{9FA5}\x{3040}-\x{309F}\x{30A0}-\x{30FF}]+`,
		`[!"#$%&'()*+,\-./:;<=>?@\[\\\]^_` + "`" + `{|}~][A-Za-z]+|[^\r\n\p{L}\p{P}\p{S}]?[\p{L}\p{M}]+| ?[\p{P}\p{S}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`,
	}
	preTokenizers := make([]*regexp2.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp2.Compile(pattern, regexp2.None)
		if err != nil {
			return nil, fmt.Errorf("编译 DeepSeek V4 预分词表达式: %w", err)
		}
		preTokenizers = append(preTokenizers, re)
	}

	return &deepSeekTokenizer{
		merges:        merges,
		addedTokens:   trie,
		preTokenizers: preTokenizers,
		byteEncoder:   deepSeekByteEncoder(),
	}, nil
}

func (t *deepSeekTokenTrie) add(value string) {
	node := t
	for _, r := range value {
		if node.children == nil {
			node.children = make(map[rune]*deepSeekTokenTrie)
		}
		child := node.children[r]
		if child == nil {
			child = &deepSeekTokenTrie{children: make(map[rune]*deepSeekTokenTrie)}
			node.children[r] = child
		}
		node = child
	}
	node.terminal = true
}

func (t *deepSeekTokenTrie) longest(input []rune, start int) int {
	node := t
	longest := 0
	for index := start; index < len(input); index++ {
		node = node.children[input[index]]
		if node == nil {
			break
		}
		if node.terminal {
			longest = index - start + 1
		}
	}
	return longest
}

func deepSeekByteEncoder() [256]rune {
	var result [256]rune
	used := make(map[int]bool, 256)
	values := make([]int, 0, 256)
	for value := 33; value <= 126; value++ {
		values = append(values, value)
		used[value] = true
	}
	for value := 161; value <= 172; value++ {
		values = append(values, value)
		used[value] = true
	}
	for value := 174; value <= 255; value++ {
		values = append(values, value)
		used[value] = true
	}
	next := 0
	for value := 0; value <= 255; value++ {
		if !used[value] {
			result[value] = rune(256 + next)
			next++
		}
	}
	for _, value := range values {
		result[value] = rune(value)
	}
	return result
}

func (t *deepSeekTokenizer) Count(input string) (int64, error) {
	if t == nil || t.addedTokens == nil {
		return 0, fmt.Errorf("DeepSeek V4 tokenizer 未初始化")
	}
	runes := []rune(input)
	var count int64
	plainStart := 0
	for index := 0; index < len(runes); {
		matched := t.addedTokens.longest(runes, index)
		if matched == 0 {
			index++
			continue
		}
		plainCount, err := t.countPlain(string(runes[plainStart:index]))
		if err != nil {
			return 0, err
		}
		count += plainCount + 1
		index += matched
		plainStart = index
	}
	plainCount, err := t.countPlain(string(runes[plainStart:]))
	if err != nil {
		return 0, err
	}
	return count + plainCount, nil
}

func (t *deepSeekTokenizer) countPlain(input string) (int64, error) {
	if input == "" {
		return 0, nil
	}
	segments := []string{input}
	for _, re := range t.preTokenizers {
		next := make([]string, 0, len(segments))
		for _, segment := range segments {
			parts, err := splitDeepSeekIsolated(re, segment)
			if err != nil {
				return 0, err
			}
			next = append(next, parts...)
		}
		segments = next
	}

	var count int64
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		encoded := make([]rune, 0, len(segment))
		for _, value := range []byte(segment) {
			encoded = append(encoded, t.byteEncoder[value])
		}
		count += int64(t.countBPE(encoded))
	}
	return count, nil
}

func splitDeepSeekIsolated(re *regexp2.Regexp, input string) ([]string, error) {
	if input == "" {
		return nil, nil
	}
	runes := []rune(input)
	match, err := re.FindStringMatch(input)
	if err != nil {
		return nil, fmt.Errorf("执行 DeepSeek V4 预分词: %w", err)
	}
	if match == nil {
		return []string{input}, nil
	}
	result := make([]string, 0, 8)
	position := 0
	for match != nil {
		if match.Index > position {
			result = append(result, string(runes[position:match.Index]))
		}
		if match.Length > 0 {
			result = append(result, string(runes[match.Index:match.Index+match.Length]))
		}
		position = match.Index + match.Length
		match, err = re.FindNextMatch(match)
		if err != nil {
			return nil, fmt.Errorf("执行 DeepSeek V4 预分词: %w", err)
		}
	}
	if position < len(runes) {
		result = append(result, string(runes[position:]))
	}
	return result, nil
}

func (t *deepSeekTokenizer) countBPE(piece []rune) int {
	if len(piece) == 0 {
		return 0
	}
	nodes := make([]deepSeekBPENode, len(piece))
	for index, value := range piece {
		nodes[index] = deepSeekBPENode{value: string(value), previous: index - 1, next: index + 1, active: true}
	}
	nodes[len(nodes)-1].next = -1
	queue := make(deepSeekBPEQueue, 0, len(nodes))
	pushPair := func(left int) {
		if left < 0 || !nodes[left].active || nodes[left].next < 0 {
			return
		}
		right := nodes[left].next
		rank, ok := t.merges[deepSeekPair{left: nodes[left].value, right: nodes[right].value}]
		if ok {
			heap.Push(&queue, deepSeekBPEMerge{rank: rank, left: left, right: right})
		}
	}
	for index := 0; index+1 < len(nodes); index++ {
		pushPair(index)
	}

	count := len(nodes)
	for queue.Len() > 0 {
		candidate := heap.Pop(&queue).(deepSeekBPEMerge)
		if !nodes[candidate.left].active || !nodes[candidate.right].active || nodes[candidate.left].next != candidate.right {
			continue
		}
		currentRank, ok := t.merges[deepSeekPair{left: nodes[candidate.left].value, right: nodes[candidate.right].value}]
		if !ok || currentRank != candidate.rank {
			continue
		}

		left := candidate.left
		right := candidate.right
		nodes[left].value += nodes[right].value
		nodes[left].next = nodes[right].next
		if nodes[right].next >= 0 {
			nodes[nodes[right].next].previous = left
		}
		nodes[right].active = false
		count--
		pushPair(nodes[left].previous)
		pushPair(left)
	}
	return count
}

type deepSeekBPENode struct {
	value          string
	previous, next int
	active         bool
}

type deepSeekBPEMerge struct {
	rank        int
	left, right int
}

type deepSeekBPEQueue []deepSeekBPEMerge

func (q deepSeekBPEQueue) Len() int { return len(q) }
func (q deepSeekBPEQueue) Less(i, j int) bool {
	if q[i].rank == q[j].rank {
		return q[i].left < q[j].left
	}
	return q[i].rank < q[j].rank
}
func (q deepSeekBPEQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *deepSeekBPEQueue) Push(value any) {
	*q = append(*q, value.(deepSeekBPEMerge))
}
func (q *deepSeekBPEQueue) Pop() any {
	old := *q
	last := old[len(old)-1]
	*q = old[:len(old)-1]
	return last
}

func estimateDeepSeekV4Input(request provider.ChatRequest) (int64, error) {
	tokenizer, err := loadDeepSeekV4Tokenizer()
	if err != nil {
		return 0, err
	}
	prompt, err := renderDeepSeekV4Prompt(request)
	if err != nil {
		return 0, err
	}
	return tokenizer.Count(prompt)
}

func renderDeepSeekV4Prompt(request provider.ChatRequest) (string, error) {
	messages := mergeDeepSeekV4ToolMessages(request.Messages)
	thinking := request.Thinking != nil && request.Thinking.Enabled
	hasTools := len(request.Tools) > 0
	lastUser := -1
	for index := range messages {
		if messages[index].Role == "user" {
			lastUser = index
		}
	}

	var prompt strings.Builder
	prompt.WriteString(deepSeekV4BOS)
	toolsRendered := false
	for index, message := range messages {
		switch message.Role {
		case "system":
			prompt.WriteString(message.Content)
			if !toolsRendered && hasTools {
				prompt.WriteString("\n\n")
				prompt.WriteString(renderDeepSeekV4Tools(request.Tools))
				toolsRendered = true
			}
		case "user":
			prompt.WriteString(deepSeekV4User)
			prompt.WriteString(message.Content)
			if nextDeepSeekV4Role(messages, index) == "assistant" || index == len(messages)-1 {
				prompt.WriteString(deepSeekV4Assistant)
				if thinking && (hasTools || index >= lastUser) {
					prompt.WriteString(deepSeekV4ThinkStart)
				} else {
					prompt.WriteString(deepSeekV4ThinkEnd)
				}
			}
		case "assistant":
			if thinking && (hasTools || index > lastUser) {
				prompt.WriteString(message.ReasoningContent)
				prompt.WriteString(deepSeekV4ThinkEnd)
			}
			prompt.WriteString(message.Content)
			if len(message.ToolCalls) > 0 {
				prompt.WriteString(renderDeepSeekV4ToolCalls(message.ToolCalls))
			}
			prompt.WriteString(deepSeekV4EOS)
		}
	}
	if !toolsRendered && hasTools {
		// OpenAI 兼容请求将 tools 放在顶层；官方编码等价地把工具说明附加到系统提示。
		content := renderDeepSeekV4Tools(request.Tools)
		return deepSeekV4BOS + content + strings.TrimPrefix(prompt.String(), deepSeekV4BOS), nil
	}
	return prompt.String(), nil
}

func nextDeepSeekV4Role(messages []provider.Message, index int) string {
	if index+1 >= len(messages) {
		return ""
	}
	return messages[index+1].Role
}

func mergeDeepSeekV4ToolMessages(messages []provider.Message) []provider.Message {
	merged := make([]provider.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "user" && len(merged) > 0 && merged[len(merged)-1].Role == "user" {
			merged[len(merged)-1].Content += "\n\n" + message.Content
			continue
		}
		if message.Role != "tool" {
			merged = append(merged, message)
			continue
		}
		content := "<tool_result>" + message.Content + "</tool_result>"
		if len(merged) > 0 && merged[len(merged)-1].Role == "user" {
			merged[len(merged)-1].Content += "\n\n" + content
		} else {
			merged = append(merged, provider.Message{Role: "user", Content: content})
		}
	}
	return merged
}

const deepSeekV4ToolsTemplate = `## Tools

You have access to a set of tools to help answer the user's question. You can invoke tools by writing a "<｜DSML｜tool_calls>" block like the following:

<｜DSML｜tool_calls>
<｜DSML｜invoke name="$TOOL_NAME">
<｜DSML｜parameter name="$PARAMETER_NAME" string="true|false">$PARAMETER_VALUE</｜DSML｜parameter>
...
</｜DSML｜invoke>
<｜DSML｜invoke name="$TOOL_NAME2">
...
</｜DSML｜invoke>
</｜DSML｜tool_calls>

String parameters should be specified as is and set ` + "`" + `string="true"` + "`" + `. For all other types (numbers, booleans, arrays, objects), pass the value in JSON format and set ` + "`" + `string="false"` + "`" + `.

If thinking_mode is enabled (triggered by <think>), you MUST output your complete reasoning inside <think>...</think> BEFORE any tool calls or final response.

Otherwise, output directly after </think> with tool calls or final response.

### Available Tool Schemas

%s

You MUST strictly follow the above defined tool name and parameter schemas to invoke tool calls.
`

func renderDeepSeekV4Tools(tools []provider.ToolDefinition) string {
	definitions := make([]string, 0, len(tools))
	for _, tool := range tools {
		definition := "{\"name\": " + quoteDeepSeekJSON(tool.Function.Name) +
			", \"description\": " + quoteDeepSeekJSON(tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			if parameters, err := formatDeepSeekJSON(tool.Function.Parameters); err == nil {
				definition += ", \"parameters\": " + parameters
			}
		}
		definitions = append(definitions, definition+"}")
	}
	return fmt.Sprintf(deepSeekV4ToolsTemplate, strings.Join(definitions, "\n"))
}

func renderDeepSeekV4ToolCalls(calls []provider.ToolCall) string {
	var output strings.Builder
	output.WriteString("\n\n<" + deepSeekV4DSML + "tool_calls>\n")
	for index, call := range calls {
		if index > 0 {
			output.WriteByte('\n')
		}
		output.WriteString("<" + deepSeekV4DSML + "invoke name=\"")
		output.WriteString(call.Function.Name)
		output.WriteString("\">\n")
		output.WriteString(renderDeepSeekV4Arguments(call.Function.Arguments))
		output.WriteString("\n</" + deepSeekV4DSML + "invoke>")
	}
	output.WriteString("\n</" + deepSeekV4DSML + "tool_calls>")
	return output.String()
}

func renderDeepSeekV4Arguments(arguments string) string {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Sprintf("<%sparameter name=\"arguments\" string=\"true\">%s</%sparameter>", deepSeekV4DSML, arguments, deepSeekV4DSML)
	}
	parts := make([]string, 0, 4)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			break
		}
		key, _ := keyToken.(string)
		value, valueErr := decodeDeepSeekJSON(decoder)
		if valueErr != nil {
			break
		}
		stringValue, isString := value.(string)
		if !isString {
			stringValue = encodeDeepSeekJSON(value)
		}
		parts = append(parts, fmt.Sprintf("<%sparameter name=\"%s\" string=\"%t\">%s</%sparameter>", deepSeekV4DSML, key, isString, stringValue, deepSeekV4DSML))
	}
	return strings.Join(parts, "\n")
}

func formatDeepSeekJSON(data []byte) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	value, err := decodeDeepSeekJSON(decoder)
	if err != nil {
		return "", err
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return "", fmt.Errorf("JSON 包含额外内容")
	}
	return encodeDeepSeekJSON(value), nil
}

type deepSeekJSONObjectEntry struct {
	key   string
	value any
}

type deepSeekJSONObject []deepSeekJSONObjectEntry

func decodeDeepSeekJSON(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(deepSeekJSONObject, 0)
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("JSON 对象键不是字符串")
			}
			value, valueErr := decodeDeepSeekJSON(decoder)
			if valueErr != nil {
				return nil, valueErr
			}
			object = append(object, deepSeekJSONObjectEntry{key: key, value: value})
		}
		if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim('}') {
			return nil, fmt.Errorf("JSON 对象未结束")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, valueErr := decodeDeepSeekJSON(decoder)
			if valueErr != nil {
				return nil, valueErr
			}
			array = append(array, value)
		}
		if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim(']') {
			return nil, fmt.Errorf("JSON 数组未结束")
		}
		return array, nil
	default:
		return nil, fmt.Errorf("不支持的 JSON 分隔符 %q", delimiter)
	}
}

func encodeDeepSeekJSON(value any) string {
	switch typed := value.(type) {
	case deepSeekJSONObject:
		parts := make([]string, 0, len(typed))
		for _, entry := range typed {
			parts = append(parts, quoteDeepSeekJSON(entry.key)+": "+encodeDeepSeekJSON(entry.value))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, encodeDeepSeekJSON(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string:
		return quoteDeepSeekJSON(typed)
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}

func quoteDeepSeekJSON(value string) string {
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(output.String(), "\n")
}

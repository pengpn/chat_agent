// Package knowledgelex 为知识库关键词检索提供可版本化的轻量分词规则。
package knowledgelex

import (
	"strings"
	"unicode"
)

const Version = 1
const MaxQueryTerms = 64

var stopWords = map[string]bool{
	"什么": true, "怎么": true, "如何": true, "是否": true, "可以": true,
	"请问": true, "一下": true, "需要": true, "多久": true,
	"the": true, "a": true, "an": true, "is": true, "are": true,
	"what": true, "how": true, "can": true, "to": true, "of": true,
}

// IndexText 把连续中文转换为相邻二元词；英文和数字按单词保留。
// 带连接符的型号同时保留组成部分和去连接符后的整体，方便精确词召回。
func IndexText(text string) string { return strings.Join(terms(text), " ") }

func QueryTerms(text string) []string {
	result := make([]string, 0)
	seen := make(map[string]bool)
	for _, term := range terms(text) {
		if !seen[term] {
			seen[term] = true
			result = append(result, term)
			if len(result) == MaxQueryTerms {
				break
			}
		}
	}
	return result
}

// TSQuery 只拼接分词器生成的字母/数字词，不接收用户输入的查询运算符。
// 返回值仍通过 SQL 参数传入数据库；OR 用于召回，排名交给 PostgreSQL。
func TSQuery(text string) string {
	queryTerms := QueryTerms(text)
	for i, term := range queryTerms {
		queryTerms[i] = "'" + term + "'"
	}
	return strings.Join(queryTerms, " | ")
}

func terms(text string) []string {
	runes := []rune(strings.ToLower(text))
	result := make([]string, 0)
	add := func(term string) {
		if term != "" && !stopWords[term] {
			result = append(result, term)
		}
	}
	for i := 0; i < len(runes); {
		if isCJK(runes[i]) {
			end := i + 1
			for end < len(runes) && isCJK(runes[end]) {
				end++
			}
			if end-i == 1 {
				add(string(runes[i:end]))
			} else {
				for j := i; j+1 < end; j++ {
					add(string(runes[j : j+2]))
				}
			}
			i = end
			continue
		}
		if !unicode.IsLetter(runes[i]) && !unicode.IsDigit(runes[i]) {
			i++
			continue
		}
		var combined strings.Builder
		parts := 0
		for i < len(runes) {
			start := i
			for i < len(runes) && !isCJK(runes[i]) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i])) && i-start < 128 {
				i++
			}
			if i == start {
				break
			}
			part := string(runes[start:i])
			add(part)
			combined.WriteString(part)
			parts++
			if i+1 >= len(runes) || !strings.ContainsRune("-_.", runes[i]) || isCJK(runes[i+1]) || (!unicode.IsLetter(runes[i+1]) && !unicode.IsDigit(runes[i+1])) || combined.Len() > 128 {
				break
			}
			i++
		}
		if parts > 1 && combined.Len() <= 512 {
			add(combined.String())
		}
	}
	return result
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

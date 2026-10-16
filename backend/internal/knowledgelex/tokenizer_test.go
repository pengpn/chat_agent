package knowledgelex

import (
	"slices"
	"strings"
	"testing"
)

func TestQueryTermsHandleChineseIdentifiersAndOperators(t *testing.T) {
	got := QueryTerms("请问如何申请退款，Qwen3-VL-Flash 和 SLA_2026？退款！")
	for _, want := range []string{"申请", "请退", "退款", "qwen3", "vl", "flash", "qwen3vlflash", "sla2026"} {
		if !slices.Contains(got, want) {
			t.Fatalf("缺少关键词 %q: %v", want, got)
		}
	}
	if slices.Contains(got, "如何") || strings.Count(strings.Join(got, ","), "退款") != 1 {
		t.Fatalf("停用词或去重失败: %v", got)
	}
	query := TSQuery("foo' | !bar:* & (baz)")
	if query != "'foo' | 'bar' | 'baz'" {
		t.Fatalf("用户运算符不应进入查询: %s", query)
	}
}

func TestTokenizerBoundsQueryAndHandlesEmptyInput(t *testing.T) {
	if TSQuery(" !!! ") != "" || IndexText("") != "" {
		t.Fatal("空白和符号输入应返回空结果")
	}
	var text strings.Builder
	for i := 0; i < 100; i++ {
		text.WriteRune(rune('一' + i))
	}
	if len(QueryTerms(text.String())) != MaxQueryTerms {
		t.Fatal("查询词数量未按上限截断")
	}
	if IndexText(strings.Repeat("x", 10000)) == "" {
		t.Fatal("长单词应安全切分，不能陷入死循环")
	}
}

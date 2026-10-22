// Package websearch 封装固定上游的联网检索，不允许模型指定任意请求地址。
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}
type Searcher interface {
	Search(context.Context, string) ([]Result, error)
}
type Tavily struct {
	key      string
	max      int
	endpoint string
	client   *http.Client
}

func NewTavily(key string, max int, timeout time.Duration) *Tavily {
	if max < 1 || max > 10 {
		max = 5
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Tavily{key, max, "https://api.tavily.com/search", &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (t *Tavily) Search(ctx context.Context, query string) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 500 {
		return nil, fmt.Errorf("搜索词需要 1–500 个字符")
	}
	input, _ := json.Marshal(map[string]any{"query": query, "max_results": t.max, "search_depth": "basic", "topic": "general", "include_answer": false, "include_raw_content": false, "include_images": false, "auto_parameters": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("联网搜索连接失败或超时")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("联网搜索上游返回状态 %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, fmt.Errorf("联网搜索响应过大或读取失败")
	}
	var payload struct {
		Results []Result `json:"results"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("联网搜索响应格式错误")
	}
	results := make([]Result, 0, t.max)
	for _, r := range payload.Results {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
			continue
		}
		r.Title = clip(r.Title, 160)
		r.Content = clip(r.Content, 800)
		r.URL = clip(r.URL, 2048)
		results = append(results, r)
		if len(results) == t.max {
			break
		}
	}
	return results, nil
}
func clip(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}

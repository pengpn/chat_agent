// Package userevent 提供进程内、按用户隔离的短期事件流。
// 当前用于向浏览器推送自动标题等后台任务结果；未来替换为 Redis Stream 时，
// 可以保留 Publish/Snapshot 语义而不修改 HTTP Handler 和业务 Service。
package userevent

import (
	"encoding/json"
	"sync"
	"time"
)

// Event 是已经序列化、可以直接写入 SSE 的单个事件。
type Event struct {
	// Sequence 在单个用户范围内单调递增，用作 SSE id 和浏览器去重断点。
	Sequence int64
	// Name 对应 SSE 的 event 字段，例如 conversation.title.updated。
	Name string
	// Data 是 JSON 数据，其中也会包含 sequence，方便前端解析后保存断点。
	Data json.RawMessage
}

// Snapshot 是订阅者某一时刻看到的不可变结果。
type Snapshot struct {
	// Events 只包含 Sequence > after 且仍在缓存中的事件。
	Events []Event
	// Gap 表示客户端断点无法由当前缓存连续补齐，需要重新读取数据库权威状态。
	Gap bool
	// Wait 指向当前 notify 通道；Publish 关闭它时，所有等待者都会被广播唤醒。
	Wait <-chan struct{}
}

// Hub 是短期、按用户隔离的进程内事件中心。
//
// 它承担三项职责：
//  1. 为每个用户分配独立递增 sequence，防止跨用户读取；
//  2. 保留有限历史，让浏览器断线后按 sequence 补发；
//  3. 使用“关闭 notify 通道”的广播方式唤醒该用户的所有 SSE 订阅者。
//
// Hub 只适合单实例或本地学习环境。多实例部署时，不同实例看不到彼此内存中的事件，
// 应替换为 Redis Stream/PubSub 等共享实现。
type Hub struct {
	// mu 保护 users、sequence、events 和 notify；Hub 的所有公开方法都可并发调用。
	mu sync.Mutex
	// ttl 控制无活动用户流在内存中的保留时间。
	ttl time.Duration
	// maxEvents 限制单个用户保留的事件数，避免长时间运行后内存无限增长。
	maxEvents int
	users     map[string]*userStream
}

// userStream 是单个用户的事件序列，永远只在持有 Hub.mu 时读写。
type userStream struct {
	sequence  int64
	events    []Event
	expiresAt time.Time
	// notify 本身不传输事件，只负责通知“状态发生变化”。真正的数据通过 Snapshot 读取。
	notify chan struct{}
}

// New 创建用户事件 Hub，并为非法配置提供安全默认值。
func New(ttl time.Duration, maxEvents int) *Hub {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if maxEvents <= 0 {
		maxEvents = 1_000
	}
	return &Hub{ttl: ttl, maxEvents: maxEvents, users: make(map[string]*userStream)}
}

// Publish 将一个业务事件发布给指定用户。
// payload 会被复制后补充 sequence，调用方后续修改原 map 不会影响缓存中的 JSON。
func (h *Hub) Publish(userID, name string, payload map[string]any) {
	if h == nil || userID == "" || name == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	// 在创建/读取流前清理过期用户，控制常驻内存规模。
	h.cleanupLocked(now)
	stream := h.streamLocked(userID, now)
	// sequence 的分配、事件追加和通知切换处于同一把锁内，因此并发 Publish
	// 不会产生重复或乱序编号。
	stream.sequence++
	data := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		data[key] = value
	}
	data["sequence"] = stream.sequence
	raw, _ := json.Marshal(data)
	stream.events = append(stream.events, Event{Sequence: stream.sequence, Name: name, Data: raw})
	if len(stream.events) > h.maxEvents {
		// 只保留最新的 maxEvents。落后的客户端会在 Snapshot 中得到 Gap=true。
		stream.events = stream.events[len(stream.events)-h.maxEvents:]
	}
	stream.expiresAt = now.Add(h.ttl)
	// close 能一次唤醒所有正在 `<-snapshot.Wait` 的 SSE 连接；随后换成新通道，
	// 下一轮等待不会因为旧通道已经关闭而持续空转。
	close(stream.notify)
	stream.notify = make(chan struct{})
}

// Snapshot 返回指定用户在 after 之后仍可补发的事件，以及下一次发布的等待通道。
// 返回的是新的 Events 切片，不把 Hub 内部可变切片暴露给 Handler。
func (h *Hub) Snapshot(userID string, after int64) Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.cleanupLocked(now)
	stream, existed := h.users[userID]
	if !existed {
		stream = h.streamLocked(userID, now)
		// 客户端带着非零断点，但服务端已经没有该用户的序列，通常说明服务重启
		// 或缓存过期。不能假装连续，必须要求客户端重新同步。
		return Snapshot{Gap: after > 0, Wait: stream.notify}
	}
	stream.expiresAt = now.Add(h.ttl)
	result := Snapshot{Wait: stream.notify}
	// after 大于服务端当前序号表示服务端序列已重置；after 落在最早缓存事件之前
	// 表示中间事件已被 maxEvents 截断。这两种情况都无法安全补发。
	if after > stream.sequence || (len(stream.events) > 0 && after < stream.events[0].Sequence-1) {
		result.Gap = true
		return result
	}
	for _, event := range stream.events {
		if event.Sequence > after {
			result.Events = append(result.Events, event)
		}
	}
	return result
}

// streamLocked 获取或创建用户流。调用方必须已经持有 h.mu。
func (h *Hub) streamLocked(userID string, now time.Time) *userStream {
	if stream := h.users[userID]; stream != nil {
		return stream
	}
	stream := &userStream{expiresAt: now.Add(h.ttl), notify: make(chan struct{})}
	h.users[userID] = stream
	return stream
}

// cleanupLocked 删除 TTL 已过期的用户流。关闭 notify 可以唤醒仍在等待旧流的
// Handler，使其重新取快照并决定继续等待还是通知客户端重置。
func (h *Hub) cleanupLocked(now time.Time) {
	for userID, stream := range h.users {
		if now.After(stream.expiresAt) {
			close(stream.notify)
			delete(h.users, userID)
		}
	}
}

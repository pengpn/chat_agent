package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/gin-gonic/gin"
)

// UserEvents 建立一条“用户级”的 SSE 长连接，用于推送不属于某一条消息流的后台事件，
// 例如 conversation.title.updated。它和回答流的区别是：回答流以 messageID 为单位，
// 回答结束后连接关闭；用户事件流在整个登录期间保持，用来接收工作空间级异步变化。
//
// 断线补发流程：
//  1. 浏览器保存最后处理的 sequence；
//  2. 重连时通过 afterSequence（或标准 Last-Event-ID）传回该值；
//  3. Hub.Snapshot 只返回 sequence 更大的缓存事件；
//  4. 如果缓存已经截断或服务重启导致序号不连续，则返回 409，要求前端重新加载权威数据。
func (h *Handler) UserEvents(c *gin.Context) {
	// fetch 可以携带 Authorization，因此前端使用查询参数保存断点；这里同时兼容
	// 原生 EventSource 自动重连时发送的 Last-Event-ID。
	afterValue := c.Query("afterSequence")
	if afterValue == "" {
		afterValue = c.GetHeader("Last-Event-ID")
	}
	if afterValue == "" {
		afterValue = "0"
	}
	after, err := strconv.ParseInt(afterValue, 10, 64)
	if err != nil || after < 0 {
		response.WriteErrorResp(c, http.StatusBadRequest, "afterSequence 必须是非负整数")
		return
	}
	if h.events == nil {
		response.WriteErrorResp(c, http.StatusServiceUnavailable, "用户事件服务不可用")
		return
	}
	userID := currentUserID(c)
	// Snapshot 同时包含当前可补发事件和 Wait 通道。读取快照不会阻塞，
	// 没有新事件时 Handler 会在后面的 select 中等待 Wait 被关闭。
	snapshot := h.events.Snapshot(userID, after)
	if snapshot.Gap {
		// 此时还没有写 SSE 响应头，所以可以返回普通 JSON 409。
		// 前端收到后重新调用会话列表接口，再从 sequence=0 订阅。
		response.WriteErrorResp(c, http.StatusConflict, "用户事件缓存存在缺口，请重新同步工作空间")
		return
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	// 禁止 Nginx 等反向代理缓冲 SSE，否则事件可能积累到缓冲区满后才到达浏览器。
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	// 立即写一个 SSE 注释并 Flush，让响应头马上到达浏览器，而不是等待第一个业务事件。
	_, _ = fmt.Fprint(c.Writer, ": connected\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		// 先补发快照中所有 sequence > after 的事件。writeCachedSSE 会同时写入
		// SSE id、event 和 JSON data，并立即 Flush。
		for _, event := range snapshot.Events {
			writeCachedSSE(c.Writer, flusher, event.Sequence, event.Name, event.Data)
			after = event.Sequence
		}
		snapshot.Events = nil
		select {
		case <-c.Request.Context().Done():
			// 页面关闭、退出登录或网络断开都会取消请求，结束该连接且不泄漏 goroutine。
			return
		case <-snapshot.Wait:
			// Publish 会 close 旧 notify 通道唤醒所有订阅者；醒来后重新取快照，
			// 而不是直接共享可变事件切片，从而避免并发读写数据竞争。
			snapshot = h.events.Snapshot(userID, after)
			if snapshot.Gap {
				// SSE 响应已经开始，无法再改成 HTTP 409，因此发送显式 reset 事件后关闭。
				// 前端重新拉取会话列表并建立新连接。
				_, _ = fmt.Fprint(c.Writer, "event: stream.reset\ndata: {\"reason\":\"event_gap\"}\n\n")
				flusher.Flush()
				return
			}
		case <-keepalive.C:
			// SSE 注释不会触发业务事件，但能防止代理或负载均衡器把空闲连接回收。
			_, _ = fmt.Fprint(c.Writer, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

export interface ScrollMetrics {
  scrollTop: number
  clientHeight: number
  scrollHeight: number
}

/**
 * 判断消息视口是否仍靠近底部。
 *
 * 流式输出期间只有靠近底部的用户才需要自动跟随；一旦用户主动向上
 * 阅读历史内容，就应停止修改 scrollTop，避免新 token 抢走滚动位置。
 */
export function isNearScrollBottom(metrics: ScrollMetrics, threshold = 80) {
  return metrics.scrollHeight - metrics.scrollTop - metrics.clientHeight <= threshold
}

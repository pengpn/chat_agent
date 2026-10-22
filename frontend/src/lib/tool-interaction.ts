// 非关键操作反馈短暂显示，不混入长期对话内容。
export const TOOL_FEEDBACK_DURATION_MS = 4000

export function remainingInteractionSeconds(expiresAt: string, now: number): number {
  const deadline = Date.parse(expiresAt)
  if (!Number.isFinite(deadline) || !Number.isFinite(now)) return 0
  return Math.max(0, Math.ceil((deadline - now) / 1000))
}

export function formatInteractionCountdown(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '等待已到期'
  const remaining = Math.ceil(seconds)
  if (remaining < 60) return `剩余 ${remaining} 秒`
  return `剩余 ${Math.floor(remaining / 60)} 分 ${String(remaining % 60).padStart(2, '0')} 秒`
}

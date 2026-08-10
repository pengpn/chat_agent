import type { Conversation, Message } from '@/types'

export function mergeConversations(current: Conversation[], incoming: Conversation[]) {
  const byID = new Map(current.map((item) => [item.id, item]))
  for (const item of incoming) byID.set(item.id, item)
  return [...byID.values()].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt) || b.id.localeCompare(a.id))
}

export function mergeMessages(older: Message[], current: Message[]) {
  const byID = new Map<string, Message>()
  for (const item of [...older, ...current]) byID.set(item.id, item)
  return sortMessagesAscending([...byID.values()])
}

export function sortMessagesAscending(items: Message[]) {
  return [...items].sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
}

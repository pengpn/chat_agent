import type { Message } from '@/types'

export interface DisplayMessage {
  message: Message
  versions: Message[]
}

export function buildDisplayMessages(messages: Message[]): DisplayMessage[] {
  if (!messages.some((message) => message.parentMessageId || message.isActiveChild)) return legacyDisplayMessages(messages)
  const children = new Map<string, Message[]>()
  for (const message of messages) {
    const parentID = message.parentMessageId ?? ''
    const siblings = children.get(parentID) ?? []
    siblings.push(message)
    children.set(parentID, siblings)
  }
  for (const siblings of children.values()) siblings.sort(compareMessages)

  const loadedIDs = new Set(messages.map((message) => message.id))
  const boundaries = messages
    .filter((message) => message.isActiveChild && (!message.parentMessageId || !loadedIDs.has(message.parentMessageId)))
    .sort(compareMessages)
  const boundary = boundaries[0] ?? messages.find((message) => !message.parentMessageId || !loadedIDs.has(message.parentMessageId))
  if (!boundary) return []

  const entries: DisplayMessage[] = []
  const visited = new Set<string>()
  let parentID = boundary.parentMessageId ?? ''
  while (true) {
    const siblings = children.get(parentID) ?? []
    const active = [...siblings].reverse().find((message) => message.isActiveChild)
    if (!active || visited.has(active.id)) break
    visited.add(active.id)
    entries.push({ message: active, versions: siblings })
    parentID = active.id
  }
  return entries
}

function legacyDisplayMessages(messages: Message[]): DisplayMessage[] {
  const groups = new Map<string, Message[]>()
  for (const message of messages) {
    if (message.role !== 'assistant' || !message.replyToMessageId) continue
    const versions = groups.get(message.replyToMessageId) ?? []
    versions.push(message)
    groups.set(message.replyToMessageId, versions)
  }
  for (const versions of groups.values()) versions.sort((a, b) => (a.responseVersion || 1) - (b.responseVersion || 1))

  const seen = new Set<string>()
  return messages.flatMap((message) => {
    if (message.role !== 'assistant' || !message.replyToMessageId) return [{ message, versions: [message] }]
    if (seen.has(message.replyToMessageId)) return []
    seen.add(message.replyToMessageId)
    const versions = groups.get(message.replyToMessageId) ?? [message]
    const active = [...versions].reverse().find((item) => item.isActiveResponse) ?? versions.at(-1) ?? message
    return [{ message: active, versions }]
  })
}

function compareMessages(a: Message, b: Message) {
  return a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id)
}

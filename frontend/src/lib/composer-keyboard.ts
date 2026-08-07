export interface ComposerKeyEvent {
  key: string
  shiftKey: boolean
  isComposing?: boolean
  keyCode?: number
}

export function shouldSendComposerMessage(event: ComposerKeyEvent) {
  if (event.isComposing || event.keyCode === 229) return false
  return event.key === 'Enter' && !event.shiftKey
}

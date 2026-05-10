export interface ChatReturnTarget {
  redirectPath: string
}

const DEFAULT_CHAT_BASE_URL = 'https://chat.handsfreeclub.com'
const CHAT_CODE_PARAM = 'hfc_chat_code'

function getChatOrigin(chatBaseUrl = import.meta.env.VITE_HFC_CHAT_URL || DEFAULT_CHAT_BASE_URL): string {
  return new URL(chatBaseUrl).origin
}

export function resolveChatReturn(
  rawReturn: unknown,
  chatBaseUrl = import.meta.env.VITE_HFC_CHAT_URL || DEFAULT_CHAT_BASE_URL
): ChatReturnTarget | null {
  const value = Array.isArray(rawReturn) ? rawReturn[0] : rawReturn
  if (typeof value !== 'string' || !value.trim()) {
    return null
  }

  try {
    const target = new URL(value)
    if (target.origin !== getChatOrigin(chatBaseUrl)) {
      return null
    }

    target.searchParams.delete(CHAT_CODE_PARAM)
    const search = target.searchParams.toString()
    return {
      redirectPath: `/${search ? `?${search}` : ''}`
    }
  } catch {
    return null
  }
}

export function buildChatBridgeFallbackUrl(
  code: string,
  redirectPath: string,
  chatBaseUrl = import.meta.env.VITE_HFC_CHAT_URL || DEFAULT_CHAT_BASE_URL
): string {
  const parsed = new URL(redirectPath || '/', getChatOrigin(chatBaseUrl))
  const target = new URL('/', getChatOrigin(chatBaseUrl))
  target.search = parsed.search
  target.searchParams.set(CHAT_CODE_PARAM, code)
  return target.toString()
}

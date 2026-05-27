let fingerprintPromise: Promise<string | undefined> | null = null

export async function collectSignupDeviceFingerprint(): Promise<string | undefined> {
  if (import.meta.env.MODE === 'test' || typeof window === 'undefined') {
    return undefined
  }

  if (!fingerprintPromise) {
    fingerprintPromise = collectLocalFingerprint()
  }
  return fingerprintPromise
}

async function collectLocalFingerprint(): Promise<string | undefined> {
  const visitorId = await collectFingerprintJsVisitorId()
  if (visitorId) {
    return visitorId
  }

  const nav = navigator as Navigator & { deviceMemory?: number }
  const parts = [
    nav.userAgent,
    nav.language,
    String(nav.hardwareConcurrency || ''),
    String(nav.deviceMemory || ''),
    `${screen.width}x${screen.height}x${screen.colorDepth}`,
    Intl.DateTimeFormat().resolvedOptions().timeZone || '',
    canvasHash()
  ]
  const raw = parts.join('|')
  return await sha256Hex(raw) || normalizeFingerprint(raw)
}

async function collectFingerprintJsVisitorId(): Promise<string | undefined> {
  try {
    const mod = await import('@fingerprintjs/fingerprintjs')
    const agent = await mod.default.load()
    const result = await agent.get()
    return normalizeFingerprint(result.visitorId)
  } catch {
    return undefined
  }
}

function canvasHash(): string {
  try {
    const canvas = document.createElement('canvas')
    canvas.width = 280
    canvas.height = 60
    const ctx = canvas.getContext('2d')
    if (!ctx) return ''
    ctx.textBaseline = 'top'
    ctx.font = '16px Arial'
    ctx.fillStyle = '#f60'
    ctx.fillRect(0, 0, 80, 24)
    ctx.fillStyle = '#069'
    ctx.fillText('HandsFreeClub signup risk', 4, 8)
    return canvas.toDataURL()
  } catch {
    return ''
  }
}

function normalizeFingerprint(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  if (!normalized) return undefined
  return normalized.slice(0, 512)
}

async function sha256Hex(value: string): Promise<string | undefined> {
  try {
    const bytes = new TextEncoder().encode(value)
    const digest = await crypto.subtle.digest('SHA-256', bytes)
    return Array.from(new Uint8Array(digest))
      .map((byte) => byte.toString(16).padStart(2, '0'))
      .join('')
  } catch {
    return undefined
  }
}

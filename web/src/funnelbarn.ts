// FunnelBarn analytics integration. Config is fetched from
// /api/v1/client-config at boot — when the server has no FunnelBarn env vars
// set, the response omits the funnelbarn block and all tracking calls no-op.

let endpoint = ''
let apiKey = ''
let project = ''
let configured = false

const SESSION_KEY = 'fb_sid'
const SESSION_EXP_KEY = 'fb_sid_exp'
const SESSION_TTL = 30 * 60 * 1000 // 30 min idle

interface FBEvent {
  name: string
  session_id: string
  timestamp: string
  url?: string
  properties?: Record<string, unknown>
}

interface ClientConfig {
  funnelbarn?: { endpoint?: string; api_key?: string; project?: string }
}

function sessionId(): string {
  try {
    const now = Date.now()
    const exp = Number(localStorage.getItem(SESSION_EXP_KEY) ?? 0)
    let sid = localStorage.getItem(SESSION_KEY)
    if (!sid || now > exp) {
      sid = crypto.randomUUID()
      localStorage.setItem(SESSION_KEY, sid)
    }
    localStorage.setItem(SESSION_EXP_KEY, String(now + SESSION_TTL))
    return sid
  } catch {
    return 'unknown'
  }
}

function makeEvent(name: string, properties?: Record<string, unknown>): FBEvent {
  return {
    name,
    session_id: sessionId(),
    timestamp: new Date().toISOString(),
    url: location.href,
    properties,
  }
}

async function send(ev: FBEvent): Promise<void> {
  if (!configured) return
  try {
    await fetch(`${endpoint}/api/v1/events`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'x-funnelbarn-api-key': apiKey,
        'x-funnelbarn-project': project,
      },
      body: JSON.stringify(ev),
      keepalive: true,
    })
  } catch { /* fire-and-forget */ }
}

export async function initFunnelBarn(): Promise<void> {
  if (typeof window === 'undefined') return
  try {
    const r = await fetch('/api/v1/client-config', { credentials: 'same-origin' })
    if (!r.ok) return
    const cfg: ClientConfig = await r.json()
    const fb = cfg.funnelbarn
    if (!fb || !fb.endpoint || !fb.api_key) return
    endpoint = fb.endpoint.replace(/\/+$/, '')
    apiKey = fb.api_key
    project = fb.project || 'spanbarn'
    configured = true
    fbPage()
  } catch { /* leave unconfigured; tracking calls no-op */ }
}

export function fbPage(properties?: Record<string, unknown>): void {
  void send(makeEvent('page_view', {
    path: location.pathname,
    referrer: document.referrer || undefined,
    ...properties,
  }))
}

export function fbTrack(name: string, properties?: Record<string, unknown>): void {
  if (!name) return
  void send(makeEvent(name, properties))
}

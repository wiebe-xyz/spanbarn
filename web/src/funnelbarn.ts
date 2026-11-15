// FunnelBarn analytics integration through the @funnelbarn/js SDK. Config is
// fetched from /api/v1/client-config at boot. When the server has no FunnelBarn
// env vars set, the response omits the funnelbarn block, no client is created
// and every tracking call is a no-op.

import { FunnelBarnClient } from '@funnelbarn/js'

interface ClientConfig {
  funnelbarn?: { endpoint?: string; api_key?: string; project?: string }
}

// Pages that show API keys or the setup guide stay out of recordings.
const RECORD_EXCLUDE = ['/settings*', '/setup*', '/login*']

const ONCE_PREFIX = 'fb_once_'

let client: FunnelBarnClient | null = null
let markReady: () => void = () => {}
// Settles when initFunnelBarn is done, configured or not, so an event sent
// while the config is still loading waits for it instead of getting lost.
const ready = new Promise<void>((resolve) => { markReady = resolve })

export function environmentFor(hostname: string): string {
  if (hostname.includes('staging')) return 'staging'
  if (hostname.includes('testing')) return 'testing'
  if (hostname === 'localhost' || hostname === '127.0.0.1') return 'development'
  return 'production'
}

export async function initFunnelBarn(): Promise<void> {
  if (typeof window === 'undefined' || client) return
  try {
    const r = await fetch('/api/v1/client-config', { credentials: 'same-origin' })
    if (!r.ok) return
    const cfg: ClientConfig = await r.json()
    const fb = cfg.funnelbarn
    if (!fb || !fb.endpoint || !fb.api_key) return
    client = new FunnelBarnClient({
      apiKey: fb.api_key,
      endpoint: fb.endpoint.replace(/\/+$/, ''),
      projectName: fb.project || 'spanbarn',
      environment: environmentFor(location.hostname),
      recording: true,
      recordExclude: RECORD_EXCLUDE,
    })
    client.page({ path: location.pathname, referrer: document.referrer || undefined })
  } catch { /* leave unconfigured; tracking calls no-op */ } finally {
    markReady()
  }
}

/** Sends a conversion event. A no-op while FunnelBarn is not configured. */
export function trackConversion(name: string, properties?: Record<string, string | number | boolean>): void {
  void ready.then(() => {
    try {
      client?.track(name, properties)
    } catch { /* analytics never breaks the page */ }
  })
}

/**
 * Sends a conversion event once per browser. The marker is written only when
 * the event went out, so a deployment without FunnelBarn leaves no marker.
 */
export function trackConversionOnce(name: string, properties?: Record<string, string | number | boolean>): void {
  void ready.then(() => {
    if (!client) return
    try {
      if (localStorage.getItem(ONCE_PREFIX + name)) return
      localStorage.setItem(ONCE_PREFIX + name, '1')
    } catch { /* private mode: no marker, the event may repeat */ }
    try {
      client.track(name, properties)
    } catch { /* analytics never breaks the page */ }
  })
}

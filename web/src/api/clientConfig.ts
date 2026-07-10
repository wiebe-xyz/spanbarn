// Public runtime config served by /api/v1/client-config. Blocks for
// integrations that are not configured server-side are simply omitted, so the
// SPA can render UI conditionally without leaking secrets.

export interface ClientConfig {
  funnelbarn?: { endpoint?: string; api_key?: string; project?: string }
  oidc?: { enabled?: boolean; loginURL?: string }
  iambarn?: {
    profile_url?: string
    issuer?: string
    client_id?: string
    post_logout_redirect_uri?: string
  }
}

let cached: Promise<ClientConfig> | null = null

export function fetchClientConfig(): Promise<ClientConfig> {
  if (cached) return cached
  cached = (async () => {
    try {
      const res = await fetch('/api/v1/client-config', { credentials: 'same-origin' })
      if (!res.ok) return {}
      return (await res.json()) as ClientConfig
    } catch {
      return {}
    }
  })()
  return cached
}

// Reset is exported for tests; production code does not need it.
export function _resetClientConfigCache(): void {
  cached = null
}

// True only when the current session was established via the iambarn OIDC
// callback. Local password sessions don't have a remote profile to link to.
export function isOIDCSession(): boolean {
  if (typeof document === 'undefined') return false
  return document.cookie
    .split('; ')
    .some((c) => c.startsWith('spanbarn_auth_method=oidc'))
}

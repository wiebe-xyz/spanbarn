// Public runtime config served by /api/v1/client-config. Blocks for
// integrations that are not configured server-side are simply omitted, so the
// SPA can render UI conditionally without leaking secrets.

export interface ClientConfig {
  funnelbarn?: { endpoint?: string; api_key?: string; project?: string }
  oidc?: { enabled?: boolean; loginURL?: string }
  iambarn?: { profile_url?: string }
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

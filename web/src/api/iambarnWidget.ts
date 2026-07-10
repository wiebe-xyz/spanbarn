// Shared helpers for embedding IAMBarn's hosted web components and driving
// RP-initiated logout. All widget data flows through SpanBarn's same-origin
// /api/iam-proxy (see internal/api/iam_proxy.go) so the browser never makes a
// cross-origin credentialed request to IAMBarn — that path is blocked by
// Chrome's third-party-cookie partitioning. Only the /oauth2/end-session
// logout navigation goes to the real issuer, which is fine because it is a
// top-level navigation, not a fetch.

import { useEffect, useState } from 'react'
import { api } from './client'
import { fetchClientConfig, isOIDCSession } from './clientConfig'

// An active IAMBarn-backed session with everything the widgets + logout need.
export interface IambarnSession {
  issuer: string
  proxyUrl: string
  clientId?: string
  postLogoutRedirectUri?: string
}

let scriptPromise: Promise<void> | null = null

// Inject the hosted widget bundle once; every custom element is registered by
// the single script. Safe to call from multiple components.
export function loadWidgetScript(issuer: string): Promise<void> {
  if (!scriptPromise) {
    scriptPromise = new Promise<void>((resolve, reject) => {
      if (document.querySelector('script[data-iambarn-widget]')) {
        resolve()
        return
      }
      const el = document.createElement('script')
      el.src = `${issuer}/widget/iambarn-widget.iife.js`
      el.dataset['iambarnWidget'] = ''
      el.onload = () => resolve()
      el.onerror = () => {
        scriptPromise = null
        reject(new Error('Failed to load iambarn widget'))
      }
      document.head.appendChild(el)
    })
  }
  return scriptPromise
}

// Resolve the current IAMBarn session (null for local-password sessions) and
// preload the widget bundle so hosted elements upgrade immediately.
export function useIambarnSession(): IambarnSession | null {
  const [session, setSession] = useState<IambarnSession | null>(null)

  useEffect(() => {
    if (!isOIDCSession()) return
    let cancelled = false
    void fetchClientConfig().then((cfg) => {
      const issuer = cfg.iambarn?.issuer
      if (cancelled || !issuer) return
      void loadWidgetScript(issuer)
      setSession({
        issuer,
        proxyUrl: window.location.origin + '/api/iam-proxy',
        clientId: cfg.iambarn?.client_id,
        postLogoutRedirectUri: cfg.iambarn?.post_logout_redirect_uri,
      })
    })
    return () => {
      cancelled = true
    }
  }, [])

  return session
}

// Build the RP-initiated logout URL on the real issuer. Mirrors the hosted
// widget's own builder: client_id + post_logout_redirect_uri, no id_token_hint
// (IAMBarn relies on the browser session cookie + the client's registered
// post-logout allowlist).
export function endSessionUrl(
  issuer: string,
  opts: { clientId?: string; postLogoutRedirectUri?: string },
): string {
  const params = new URLSearchParams()
  if (opts.clientId) params.set('client_id', opts.clientId)
  if (opts.postLogoutRedirectUri) params.set('post_logout_redirect_uri', opts.postLogoutRedirectUri)
  const query = params.toString()
  return `${issuer}/oauth2/end-session${query ? `?${query}` : ''}`
}

// Log out. For an IAMBarn session this clears the local session first (best
// effort) then navigates to the issuer's end-session endpoint; IAMBarn ends its
// own session and redirects back to post_logout_redirect_uri (SpanBarn's
// /api/v1/oidc/logout-complete, which re-clears the local cookies). For local
// sessions — or if the issuer/client config is missing — it falls back to a
// plain local logout. `fallback` runs the SPA-local navigation to /login.
export async function iambarnLogout(
  session: IambarnSession | null,
  fallback: () => void,
): Promise<void> {
  try {
    await api.logout()
  } catch {
    // ignore — end-session (or the fallback) still tears the session down.
  }
  if (session?.issuer && session.clientId && session.postLogoutRedirectUri) {
    window.location.assign(
      endSessionUrl(session.issuer, {
        clientId: session.clientId,
        postLogoutRedirectUri: session.postLogoutRedirectUri,
      }),
    )
    return
  }
  fallback()
}

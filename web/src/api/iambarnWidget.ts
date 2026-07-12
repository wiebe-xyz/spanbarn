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

export interface IambarnProfile {
  name: string
  email: string
  picture: string
}

// The profile snapshot (name/email/picture) lives server-side on the session
// row (claims_json) and is served by same-origin /api/v1/me — it replaced the
// JS-readable spanbarn_iam_profile cookie of the pre-session-store design.
// Fetched once per page load and cached module-wide.
let profilePromise: Promise<IambarnProfile | null> | null = null
let cachedProfile: IambarnProfile | null = null

async function fetchIambarnProfile(): Promise<IambarnProfile | null> {
  try {
    const res = await fetch('/api/v1/me', { credentials: 'same-origin' })
    if (!res.ok) return null
    const body = (await res.json()) as {
      display_name?: string
      profile?: Partial<IambarnProfile>
    }
    if (!body.profile) return null
    cachedProfile = {
      name: body.profile.name ?? body.display_name ?? '',
      email: body.profile.email ?? '',
      picture: body.profile.picture ?? '',
    }
    return cachedProfile
  } catch {
    return null
  }
}

// useIambarnProfile resolves the header-chip profile for OIDC sessions.
// Returns null for local/e2e sessions and while the first fetch is in flight.
export function useIambarnProfile(): IambarnProfile | null {
  const [profile, setProfile] = useState<IambarnProfile | null>(cachedProfile)

  useEffect(() => {
    if (!isOIDCSession()) return
    if (!profilePromise) profilePromise = fetchIambarnProfile()
    let cancelled = false
    void profilePromise.then((p) => {
      if (!cancelled && p) setProfile(p)
    })
    return () => {
      cancelled = true
    }
  }, [])

  return profile
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

// Log out. POST /api/v1/logout destroys the server-side session row, revokes
// the stored refresh token at IAMBarn (best effort) and — for OIDC sessions —
// returns logout_url: the issuer's end-session URL with id_token_hint. The
// SPA follows it so the IAMBarn session dies too; IAMBarn then redirects back
// to post_logout_redirect_uri (SpanBarn's /api/v1/oidc/logout-complete). For
// local sessions — or if the server produced no logout_url — it falls back to
// the client-built end-session URL, else a plain local logout. `fallback`
// runs the SPA-local navigation to /login.
export async function iambarnLogout(
  session: IambarnSession | null,
  fallback: () => void,
): Promise<void> {
  let logoutUrl = ''
  try {
    const res = await api.logout()
    logoutUrl = res.logout_url ?? ''
  } catch {
    // ignore — end-session (or the fallback) still tears the session down.
  }
  if (!logoutUrl && session?.issuer && session.clientId && session.postLogoutRedirectUri) {
    logoutUrl = endSessionUrl(session.issuer, {
      clientId: session.clientId,
      postLogoutRedirectUri: session.postLogoutRedirectUri,
    })
  }
  if (logoutUrl) {
    window.location.assign(logoutUrl)
    return
  }
  fallback()
}

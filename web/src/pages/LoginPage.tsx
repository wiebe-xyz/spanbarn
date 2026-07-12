import { useEffect, useState, type FormEvent, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { Activity } from 'lucide-react'
import { api, ApiError } from '../api/client'

interface OIDCClientConfig {
  oidc?: { enabled?: boolean; loginURL?: string }
}

// maybeRedirectToOIDC fetches /api/v1/client-config and, when the server
// reports oidc.enabled, redirects the browser to the OIDC login URL. When
// OIDC is not configured the call no-ops and the local password form is shown.
async function maybeRedirectToOIDC(): Promise<boolean> {
  try {
    const res = await fetch('/api/v1/client-config', { credentials: 'same-origin' })
    if (!res.ok) return false
    const cfg = (await res.json()) as OIDCClientConfig
    const oc = cfg?.oidc
    if (oc?.enabled && oc.loginURL) {
      window.location.assign(oc.loginURL)
      return true
    }
  } catch {
    // Network error — fall back to the local login form silently.
  }
  return false
}

export function LoginPage(): ReactElement {
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  // Set by the post-logout landing (/login?logged_out=1): show a signed-out
  // state instead of auto-restarting OIDC, which would bounce the just-signed-out
  // user straight back to the IdP login.
  const loggedOut = new URLSearchParams(window.location.search).has('logged_out')
  const [redirecting, setRedirecting] = useState(!loggedOut)

  useEffect(() => {
    if (loggedOut) return // signed-out landing — do not auto-restart OIDC
    let cancelled = false
    void maybeRedirectToOIDC().then((redirected) => {
      if (!cancelled && !redirected) setRedirecting(false)
    })
    return () => {
      cancelled = true
    }
  }, [loggedOut])

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      await api.login(username, password)
      navigate('/', { replace: true })
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setError('Invalid username or password')
      } else {
        setError('Something went wrong. Please try again.')
      }
    } finally {
      setSubmitting(false)
    }
  }

  if (redirecting) {
    return (
      <div
        style={{
          minHeight: '100vh',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          padding: '1rem',
          color: 'var(--text-muted)',
        }}
      >
        Loading…
      </div>
    )
  }

  if (loggedOut) {
    return (
      <div
        style={{
          minHeight: '100vh',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          padding: '1rem',
        }}
      >
        <div
          style={{
            width: '100%',
            maxWidth: 400,
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 16,
            padding: '2.5rem',
            textAlign: 'center',
            boxShadow: '0 25px 60px rgba(0,0,0,0.5)',
          }}
        >
          <Activity size={40} color="var(--accent)" style={{ marginBottom: 8 }} />
          <div style={{ fontSize: 26, fontWeight: 800, letterSpacing: '-0.5px' }}>
            Span<span style={{ color: 'var(--accent)' }}>Barn</span>
          </div>
          <div style={{ fontSize: 15, color: 'var(--text-muted)', margin: '1.25rem 0 1.75rem' }}>
            You've been signed out.
          </div>
          <a
            href="/api/v1/oidc/login"
            className="btn btn-primary"
            style={{
              display: 'inline-flex',
              width: '100%',
              justifyContent: 'center',
              padding: '0.75rem',
              fontSize: '0.9375rem',
              fontWeight: 700,
              // Explicit: as an <a>, the link color would otherwise override the
              // button's white text, rendering it blue-on-blue (illegible).
              color: '#fff',
              textDecoration: 'none',
            }}
          >
            Sign in
          </a>
        </div>
      </div>
    )
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: '1rem',
      }}
    >
      {/* Background grid */}
      <div
        style={{
          position: 'fixed',
          inset: 0,
          backgroundImage: 'radial-gradient(circle, #2a2d3a 1px, transparent 1px)',
          backgroundSize: '32px 32px',
          opacity: 0.4,
          pointerEvents: 'none',
        }}
      />

      <div
        style={{
          position: 'relative',
          width: '100%',
          maxWidth: 400,
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderRadius: 16,
          padding: '2.5rem',
          boxShadow: '0 25px 60px rgba(0,0,0,0.5)',
        }}
      >
        {/* Logo */}
        <div style={{ textAlign: 'center', marginBottom: '2rem' }}>
          <Activity size={40} color="var(--accent)" style={{ marginBottom: 8 }} />
          <div style={{ fontSize: 26, fontWeight: 800, letterSpacing: '-0.5px' }}>
            Span<span style={{ color: 'var(--accent)' }}>Barn</span>
          </div>
          <div style={{ fontSize: 14, color: 'var(--text-muted)', marginTop: 6 }}>
            Distributed tracing dashboard
          </div>
        </div>

        {/* Error */}
        {error && (
          <div
            style={{
              background: 'rgba(239,68,68,0.1)',
              border: '1px solid rgba(239,68,68,0.3)',
              borderRadius: 8,
              padding: '0.75rem 1rem',
              color: 'var(--error)',
              fontSize: 14,
              marginBottom: '1.25rem',
            }}
          >
            {error}
          </div>
        )}

        {/* Form */}
        <form onSubmit={handleSubmit}>
          <div style={{ marginBottom: '1rem' }}>
            <label htmlFor="username">Username</label>
            <input
              id="username"
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              required
              autoFocus
              autoComplete="username"
            />
          </div>

          <div style={{ marginBottom: '1.5rem' }}>
            <label htmlFor="password">Password</label>
            <input
              id="password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              autoComplete="current-password"
            />
          </div>

          <button
            type="submit"
            disabled={submitting}
            className="btn btn-primary"
            style={{
              width: '100%',
              justifyContent: 'center',
              padding: '0.75rem',
              fontSize: '0.9375rem',
              fontWeight: 700,
              opacity: submitting ? 0.7 : 1,
            }}
          >
            {submitting ? 'Signing in...' : 'Sign in'}
          </button>
        </form>
      </div>
    </div>
  )
}

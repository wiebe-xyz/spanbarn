import { useEffect, useState, type CSSProperties, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { fetchClientConfig, isOIDCSession } from '../api/clientConfig'

let scriptPromise: Promise<void> | null = null

function loadWidgetScript(src: string): Promise<void> {
  if (!scriptPromise) {
    scriptPromise = new Promise<void>((resolve, reject) => {
      if (document.querySelector('script[data-iambarn-widget]')) { resolve(); return }
      const el = document.createElement('script')
      el.src = src
      el.dataset['iambarnWidget'] = ''
      el.onload = () => resolve()
      el.onerror = () => { scriptPromise = null; reject(new Error('Failed to load widget')) }
      document.head.appendChild(el)
    })
  }
  return scriptPromise
}

const widgetStyle: CSSProperties = {
  ...({
    '--iambarn-width': '100%',
    '--iambarn-bg': 'var(--surface)',
    '--iambarn-surface': 'var(--surface-hover)',
    '--iambarn-border': 'var(--border)',
    '--iambarn-text': 'var(--text)',
    '--iambarn-muted': 'var(--text-muted)',
  } as CSSProperties),
  border: 'none',
  borderRadius: 0,
  width: '100%',
}

type Status = 'loading' | 'ready' | 'needs-reauth'

export function ProfilePage(): ReactElement {
  const navigate = useNavigate()
  const [status, setStatus] = useState<Status>('loading')

  useEffect(() => {
    if (!isOIDCSession()) { navigate('/', { replace: true }); return }

    void (async () => {
      // Probe the proxy to check if our stored access token is still valid.
      try {
        const r = await fetch('/api/iam-proxy/api/v1/me', { credentials: 'include' })
        if (r.status === 401) {
          setStatus('needs-reauth')
          return
        }
      } catch {
        setStatus('needs-reauth')
        return
      }

      void fetchClientConfig().then((cfg) => {
        const issuer = cfg.iambarn?.issuer
        if (issuer) void loadWidgetScript(`${issuer}/widget/iambarn-widget.iife.js`)
      })
      setStatus('ready')
    })()
  }, [navigate])

  // Redirect through a fresh OIDC loop when the access token has expired.
  // IamBarn re-issues tokens silently using the existing session.
  useEffect(() => {
    if (status === 'needs-reauth') {
      window.location.href = '/api/v1/oidc/login?next=/profile'
    }
  }, [status])

  if (!isOIDCSession()) return <></>

  const proxyUrl = window.location.origin + '/api/iam-proxy'

  return (
    <div style={{ maxWidth: 560, margin: '2rem auto', padding: '0 1rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1.5rem' }}>
        <button
          onClick={() => navigate(-1)}
          className="btn"
          style={{ background: 'transparent', border: 'none', color: 'var(--text-muted)' }}
        >
          <ArrowLeft size={18} />
        </button>
        <h1 style={{ fontSize: '1.25rem', fontWeight: 600, margin: 0 }}>Account</h1>
      </div>

      {status === 'loading' && (
        <div style={{ color: 'var(--text-muted)', fontSize: '0.875rem', padding: '1rem 0' }}>
          Loading…
        </div>
      )}

      {status === 'ready' && (
        <div
          style={{
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: '0.75rem',
            overflow: 'hidden',
          }}
        >
          <iambarn-profile server-url={proxyUrl} style={widgetStyle} />
        </div>
      )}
    </div>
  )
}

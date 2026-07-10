import { useEffect, useState, type CSSProperties, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, RefreshCw } from 'lucide-react'
import { fetchClientConfig, isOIDCSession } from '../api/clientConfig'
import { loadWidgetScript } from '../api/iambarnWidget'

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
        if (issuer) void loadWidgetScript(issuer)
      })
      setStatus('ready')
    })()
  }, [navigate])

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

      {status === 'needs-reauth' && (
        <div
          style={{
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: '0.75rem',
            padding: '2rem',
            textAlign: 'center',
          }}
        >
          <p style={{ color: 'var(--text-muted)', marginBottom: '1rem', fontSize: '0.875rem' }}>
            Your session has expired. Sign in again to manage your account.
          </p>
          <a
            href={'/api/v1/oidc/login?next=/account'}
            className="btn"
            style={{ textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: '0.5rem' }}
          >
            <RefreshCw size={15} />
            Sign in again
          </a>
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

import { useEffect, type CSSProperties, type ReactElement } from 'react'
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

export function ProfilePage(): ReactElement {
  const navigate = useNavigate()

  useEffect(() => {
    if (!isOIDCSession()) return
    void fetchClientConfig().then((cfg) => {
      const issuer = cfg.iambarn?.issuer
      if (issuer) void loadWidgetScript(`${issuer}/widget/iambarn-widget.iife.js`)
    })
  }, [])

  if (!isOIDCSession()) {
    navigate('/', { replace: true })
    return <></>
  }

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
    </div>
  )
}

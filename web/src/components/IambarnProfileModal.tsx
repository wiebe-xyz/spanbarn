import { useEffect, type CSSProperties, type ReactElement } from 'react'
import { LogOut } from 'lucide-react'

let scriptPromise: Promise<void> | null = null

function loadWidgetScript(src: string): Promise<void> {
  if (!scriptPromise) {
    scriptPromise = new Promise<void>((resolve, reject) => {
      if (document.querySelector('script[data-iambarn-widget]')) {
        resolve()
        return
      }
      const el = document.createElement('script')
      el.src = src
      el.dataset['iambarnWidget'] = ''
      el.onload = () => resolve()
      el.onerror = () => {
        scriptPromise = null
        reject(new Error('Failed to load IamBarn widget'))
      }
      document.head.appendChild(el)
    })
  }
  return scriptPromise
}

const widgetStyle: CSSProperties = {
  // Override the fixed 480px width and merge with SpanBarn's CSS vars.
  // CSSProperties cast is required for custom properties.
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

interface Props {
  issuer: string
  onClose: () => void
  onLogout: () => void
}

export function IambarnProfileModal({ issuer, onClose, onLogout }: Props): ReactElement {
  useEffect(() => {
    void loadWidgetScript(`${issuer}/widget/iambarn-widget.iife.js`)
  }, [issuer])

  return (
    <>
      <div
        style={{
          position: 'fixed',
          inset: 0,
          background: 'rgba(0,0,0,0.5)',
          zIndex: 1000,
        }}
        onClick={onClose}
      />
      <div
        style={{
          position: 'fixed',
          zIndex: 1001,
          top: '50%',
          left: '50%',
          transform: 'translate(-50%, -50%)',
          width: 'min(520px, calc(100vw - 2rem))',
          maxHeight: 'calc(100vh - 4rem)',
          overflowY: 'auto',
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderRadius: '0.75rem',
          boxShadow: '0 8px 40px rgba(0,0,0,0.4)',
        }}
      >
        <iambarn-profile server-url={issuer} style={widgetStyle} />
        <div
          style={{
            padding: '0.75rem 1rem',
            borderTop: '1px solid var(--border)',
          }}
        >
          <button
            className="btn"
            onClick={onLogout}
            style={{
              width: '100%',
              justifyContent: 'center',
              background: 'transparent',
              border: 'none',
              color: 'var(--text-muted)',
            }}
          >
            <LogOut size={16} />
            Logout
          </button>
        </div>
      </div>
    </>
  )
}

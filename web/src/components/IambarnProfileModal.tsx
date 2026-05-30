import { useEffect, useRef, useState, type CSSProperties, type ReactElement } from 'react'
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
  proxyUrl: string
  triggerRef: React.RefObject<HTMLButtonElement | null>
  onClose: () => void
  onLogout: () => void
}

export function IambarnProfileModal({ issuer, proxyUrl, triggerRef, onClose, onLogout }: Props): ReactElement {
  const [pos, setPos] = useState({ bottom: 64, left: 8, width: 268 })
  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    void loadWidgetScript(`${issuer}/widget/iambarn-widget.iife.js`)
  }, [issuer])

  useEffect(() => {
    const el = triggerRef.current
    if (!el) return
    const rect = el.getBoundingClientRect()
    setPos({
      // 8px gap above the trigger button
      bottom: window.innerHeight - rect.top + 8,
      left: rect.left,
      // match sidebar width
      width: Math.max(rect.width, 260),
    })
  }, [triggerRef])

  // Close on outside click
  useEffect(() => {
    const handleClick = (e: MouseEvent) => {
      const target = e.target as Node
      if (panelRef.current && !panelRef.current.contains(target) &&
          triggerRef.current && !triggerRef.current.contains(target)) {
        onClose()
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [onClose, triggerRef])

  return (
    <div
      ref={panelRef}
      style={{
        position: 'fixed',
        zIndex: 1000,
        bottom: pos.bottom,
        left: pos.left,
        width: pos.width,
        maxHeight: 'calc(100vh - 80px)',
        overflowY: 'auto',
        background: 'var(--surface)',
        border: '1px solid var(--border)',
        borderRadius: '0.75rem',
        boxShadow: '0 4px 24px rgba(0,0,0,0.3)',
      }}
    >
      <iambarn-profile server-url={proxyUrl} style={widgetStyle} />
      <div
        style={{
          padding: '0.5rem 0.75rem',
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
  )
}

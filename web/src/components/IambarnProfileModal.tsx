import { useEffect, useRef, useState, type ReactElement } from 'react'
import { LogOut, UserCog } from 'lucide-react'
import { useNavigate } from 'react-router-dom'

interface Props {
  issuer: string
  proxyUrl: string
  triggerRef: React.RefObject<HTMLButtonElement | null>
  onClose: () => void
  onLogout: () => void
}

// issuer and proxyUrl are kept for API compatibility but the widget is now
// on the dedicated /profile page rather than inline in this popover.
export function IambarnProfileModal({ issuer: _issuer, proxyUrl: _proxyUrl, triggerRef, onClose, onLogout }: Props): ReactElement { // eslint-disable-line @typescript-eslint/no-unused-vars
  const [pos, setPos] = useState({ bottom: 64, left: 8, width: 220 })
  const panelRef = useRef<HTMLDivElement>(null)
  const navigate = useNavigate()

  useEffect(() => {
    const el = triggerRef.current
    if (!el) return
    const rect = el.getBoundingClientRect()
    setPos({
      bottom: window.innerHeight - rect.top + 8,
      left: rect.left,
      width: Math.max(rect.width, 200),
    })
  }, [triggerRef])

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

  const menuItem = {
    display: 'flex',
    alignItems: 'center',
    gap: '0.625rem',
    width: '100%',
    padding: '0.625rem 0.875rem',
    background: 'transparent',
    border: 'none',
    color: 'var(--text-muted)',
    fontSize: '0.875rem',
    cursor: 'pointer',
    borderRadius: '0.5rem',
    textDecoration: 'none',
  } as const

  return (
    <div
      ref={panelRef}
      style={{
        position: 'fixed',
        zIndex: 1000,
        bottom: pos.bottom,
        left: pos.left,
        width: pos.width,
        background: 'var(--surface)',
        border: '1px solid var(--border)',
        borderRadius: '0.75rem',
        boxShadow: '0 4px 24px rgba(0,0,0,0.3)',
        padding: '0.375rem',
      }}
    >
      <button
        style={{ ...menuItem, color: 'var(--text)' }}
        onClick={() => { onClose(); navigate('/profile') }}
      >
        <UserCog size={16} />
        Account settings
      </button>
      <div style={{ height: 1, background: 'var(--border)', margin: '0.25rem 0' }} />
      <button style={menuItem} onClick={onLogout}>
        <LogOut size={16} />
        Logout
      </button>
    </div>
  )
}

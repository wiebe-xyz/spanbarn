import { useState, useEffect, type ReactElement } from 'react'

interface BeforeInstallPromptEvent extends Event {
  prompt(): Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

const DISMISS_KEY = 'spanbarn-pwa-dismissed'

export function PWAInstallBanner(): ReactElement | null {
  const [deferredPrompt, setDeferredPrompt] = useState<BeforeInstallPromptEvent | null>(null)
  const [dismissed, setDismissed] = useState(() => localStorage.getItem(DISMISS_KEY) === '1')

  useEffect(() => {
    const handler = (e: Event) => {
      e.preventDefault()
      setDeferredPrompt(e as BeforeInstallPromptEvent)
    }
    window.addEventListener('beforeinstallprompt', handler)
    return () => window.removeEventListener('beforeinstallprompt', handler)
  }, [])

  if (!deferredPrompt || dismissed) return null

  const handleInstall = async () => {
    await deferredPrompt.prompt()
    const { outcome } = await deferredPrompt.userChoice
    if (outcome === 'accepted') {
      setDeferredPrompt(null)
    }
  }

  const handleDismiss = () => {
    localStorage.setItem(DISMISS_KEY, '1')
    setDismissed(true)
  }

  return (
    <div
      style={{
        position: 'fixed',
        bottom: '1rem',
        left: '50%',
        transform: 'translateX(-50%)',
        background: 'var(--surface)',
        border: '1px solid var(--border)',
        borderRadius: '0.75rem',
        padding: '0.75rem 1rem',
        display: 'flex',
        alignItems: 'center',
        gap: '0.75rem',
        zIndex: 1000,
        boxShadow: '0 4px 24px rgba(0, 0, 0, 0.4)',
        maxWidth: 'calc(100vw - 2rem)',
      }}
    >
      <span style={{ fontSize: '0.875rem', color: 'var(--text)', whiteSpace: 'nowrap' }}>
        Install SpanBarn
      </span>
      <button
        onClick={handleInstall}
        className="btn btn-primary btn-sm"
      >
        Install
      </button>
      <button
        onClick={handleDismiss}
        style={{
          background: 'none',
          border: 'none',
          color: 'var(--text-muted)',
          fontSize: '1.125rem',
          lineHeight: 1,
          padding: '0.25rem',
          cursor: 'pointer',
        }}
        aria-label="Dismiss"
      >
        &times;
      </button>
    </div>
  )
}

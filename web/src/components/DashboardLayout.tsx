import { type ReactElement, useEffect, useRef, useState } from 'react'
import { NavLink, Outlet, useNavigate, useLocation } from 'react-router-dom'
import { Activity, Bell, GitBranch, Network, Search, Database, BrainCircuit, Radio, Settings, LogOut, Globe } from 'lucide-react'
import { api } from '../api/client'
import { fetchClientConfig, isOIDCSession, fetchIambarnMe, type IambarnUser } from '../api/clientConfig'
import { IambarnProfileModal } from './IambarnProfileModal'
import { MobileTabBar } from './MobileTabBar'
import { PWAInstallBanner } from './PWAInstallBanner'

const navItems = [
  { to: '/', icon: Activity, label: 'Services' },
  { to: '/traces', icon: Search, label: 'Traces' },
  { to: '/dependencies', icon: GitBranch, label: 'Dependencies' },
  { to: '/service-map', icon: Network, label: 'Service Map' },
  { to: '/live', icon: Radio, label: 'Live Tail' },
  { to: '/database', icon: Database, label: 'Database' },
  { to: '/prompts', icon: BrainCircuit, label: 'Prompts' },
  { to: '/pages', icon: Globe, label: 'Pages' },
  { to: '/alerts', icon: Bell, label: 'Alerts' },
  { to: '/settings', icon: Settings, label: 'Settings' },
]

export function DashboardLayout(): ReactElement {
  const navigate = useNavigate()
  const location = useLocation()
  const [iambarnIssuer, setIambarnIssuer] = useState<string | null>(null)
  const [iambarnUser, setIambarnUser] = useState<IambarnUser | null>(null)
  const [profileOpen, setProfileOpen] = useState(false)
  const chipRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (navigator.serviceWorker?.controller) {
      navigator.serviceWorker.controller.postMessage({
        type: 'PREFETCH_ADJACENT',
        path: location.pathname,
      })
    }
  }, [location.pathname])

  useEffect(() => {
    // Only sessions opened via the iambarn OIDC callback have a remote
    // profile to manage — local password sessions do not.
    if (!isOIDCSession()) return
    let cancelled = false
    void fetchClientConfig().then(async (cfg) => {
      const issuer = cfg.iambarn?.issuer
      if (cancelled || !issuer) return
      setIambarnIssuer(issuer)
      const user = await fetchIambarnMe(issuer)
      if (!cancelled) setIambarnUser(user)
    })
    return () => {
      cancelled = true
    }
  }, [])

  const handleLogout = async () => {
    setProfileOpen(false)
    try {
      await api.logout()
    } catch {
      // ignore
    }
    navigate('/login', { replace: true })
  }

  return (
    <div className="dashboard-layout" style={{ display: 'flex', minHeight: '100vh' }}>
      {/* Sidebar — hidden on mobile via CSS */}
      <aside
        className="sidebar"
        style={{
          width: 220,
          background: 'var(--surface)',
          borderRight: '1px solid var(--border)',
          display: 'flex',
          flexDirection: 'column',
          flexShrink: 0,
        }}
      >
        {/* Logo */}
        <div
          style={{
            padding: '1.25rem 1rem',
            borderBottom: '1px solid var(--border)',
            display: 'flex',
            alignItems: 'center',
            gap: '0.5rem',
          }}
        >
          <Activity size={22} color="var(--accent)" />
          <span style={{ fontSize: '1.125rem', fontWeight: 700 }}>
            Span<span style={{ color: 'var(--accent)' }}>Barn</span>
          </span>
        </div>

        {/* Navigation */}
        <nav style={{ flex: 1, padding: '0.75rem 0.5rem' }}>
          {navItems.map(({ to, icon: Icon, label }) => (
            <NavLink
              key={to}
              to={to}
              end={to === '/'}
              style={({ isActive }) => ({
                display: 'flex',
                alignItems: 'center',
                gap: '0.625rem',
                padding: '0.625rem 0.75rem',
                borderRadius: '0.5rem',
                fontSize: '0.875rem',
                fontWeight: 500,
                color: isActive ? 'var(--text)' : 'var(--text-muted)',
                background: isActive ? 'var(--surface-hover)' : 'transparent',
                textDecoration: 'none',
                marginBottom: '0.125rem',
                transition: 'background 0.15s, color 0.15s',
              })}
            >
              <Icon size={18} />
              {label}
            </NavLink>
          ))}
        </nav>

        {/* User actions */}
        <div style={{ padding: '0.75rem 0.5rem', borderTop: '1px solid var(--border)' }}>
          {iambarnIssuer ? (
            <button
              ref={chipRef}
              onClick={() => setProfileOpen(true)}
              className="btn"
              style={{
                width: '100%',
                justifyContent: 'flex-start',
                background: 'transparent',
                border: 'none',
                color: 'var(--text-muted)',
                gap: '0.625rem',
              }}
            >
              {iambarnUser?.picture ? (
                <img
                  src={iambarnUser.picture}
                  alt=""
                  style={{
                    width: 28,
                    height: 28,
                    borderRadius: '50%',
                    objectFit: 'cover',
                    flexShrink: 0,
                  }}
                  onError={(e) => {
                    ;(e.currentTarget as HTMLImageElement).style.display = 'none'
                  }}
                />
              ) : (
                <div
                  style={{
                    width: 28,
                    height: 28,
                    borderRadius: '50%',
                    background: 'var(--accent)',
                    color: '#fff',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    fontSize: 12,
                    fontWeight: 600,
                    flexShrink: 0,
                  }}
                >
                  {(iambarnUser?.display_name || iambarnUser?.email || '?')[0].toUpperCase()}
                </div>
              )}
              <span
                style={{
                  fontSize: '0.875rem',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {iambarnUser?.display_name || iambarnUser?.email || 'Profile'}
              </span>
            </button>
          ) : (
            <button
              onClick={handleLogout}
              className="btn"
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
          )}
        </div>
        {profileOpen && iambarnIssuer && (
          <IambarnProfileModal
            issuer={iambarnIssuer}
            triggerRef={chipRef}
            onClose={() => setProfileOpen(false)}
            onLogout={handleLogout}
          />
        )}
      </aside>

      {/* Main content */}
      <main className="main-content" style={{ flex: 1, padding: '1.5rem 2rem', overflowY: 'auto' }}>
        <Outlet />
      </main>

      {/* Mobile tab bar — shown on mobile via CSS */}
      <MobileTabBar />
      <PWAInstallBanner />
    </div>
  )
}

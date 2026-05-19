import { type ReactElement, useEffect, useState } from 'react'
import { NavLink, Outlet, useNavigate, useLocation } from 'react-router-dom'
import { Activity, Bell, GitBranch, Network, Search, Database, BrainCircuit, Radio, Settings, LogOut, Globe, UserCog } from 'lucide-react'
import { api } from '../api/client'
import { fetchClientConfig, isOIDCSession } from '../api/clientConfig'
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
  const [iambarnProfileURL, setIambarnProfileURL] = useState<string | null>(null)

  useEffect(() => {
    if (navigator.serviceWorker?.controller) {
      navigator.serviceWorker.controller.postMessage({
        type: 'PREFETCH_ADJACENT',
        path: location.pathname,
      })
    }
  }, [location.pathname])

  useEffect(() => {
    // Only sessions actually opened via the iambarn OIDC callback should
    // see the profile link — local password sessions don't have a remote
    // profile to manage.
    if (!isOIDCSession()) return
    let cancelled = false
    void fetchClientConfig().then((cfg) => {
      if (!cancelled && cfg.iambarn?.profile_url) {
        setIambarnProfileURL(cfg.iambarn.profile_url)
      }
    })
    return () => {
      cancelled = true
    }
  }, [])

  const handleLogout = async () => {
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
          {iambarnProfileURL && (
            <a
              href={iambarnProfileURL}
              target="_blank"
              rel="noopener noreferrer"
              className="btn"
              style={{
                width: '100%',
                justifyContent: 'center',
                background: 'transparent',
                border: 'none',
                color: 'var(--text-muted)',
                marginBottom: '0.25rem',
                textDecoration: 'none',
              }}
            >
              <UserCog size={16} />
              Edit IAMBarn profile
            </a>
          )}
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
        </div>
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

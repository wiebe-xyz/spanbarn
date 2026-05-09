import { useState, type ReactElement } from 'react'
import { NavLink, useNavigate } from 'react-router-dom'
import { Activity, Search, GitBranch, BrainCircuit, MoreHorizontal, Settings, Database, Radio, Network, Globe, LogOut } from 'lucide-react'
import { api } from '../api/client'

const tabs = [
  { to: '/', icon: Activity, label: 'Services' },
  { to: '/traces', icon: Search, label: 'Traces' },
  { to: '/dependencies', icon: GitBranch, label: 'Deps' },
  { to: '/prompts', icon: BrainCircuit, label: 'Prompts' },
]

export function MobileTabBar(): ReactElement {
  const [moreOpen, setMoreOpen] = useState(false)
  const navigate = useNavigate()

  const handleLogout = async () => {
    try {
      await api.logout()
    } catch {
      // ignore
    }
    navigate('/login', { replace: true })
  }

  return (
    <>
      {/* Overflow menu backdrop */}
      {moreOpen && (
        <div
          style={{
            position: 'fixed',
            inset: 0,
            background: 'rgba(0, 0, 0, 0.5)',
            zIndex: 999,
          }}
          onClick={() => setMoreOpen(false)}
        />
      )}

      {/* Overflow menu */}
      {moreOpen && (
        <div
          className="mobile-more-menu"
          style={{
            position: 'fixed',
            bottom: 'calc(env(safe-area-inset-bottom, 0px) + 60px)',
            right: '0.5rem',
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: '0.75rem',
            padding: '0.5rem',
            zIndex: 1001,
            minWidth: '160px',
            boxShadow: '0 -4px 24px rgba(0, 0, 0, 0.4)',
          }}
        >
          <NavLink
            to="/live"
            onClick={() => setMoreOpen(false)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.625rem',
              width: '100%',
              padding: '0.625rem 0.75rem',
              borderRadius: '0.5rem',
              fontSize: '0.875rem',
              fontWeight: 500,
              color: 'var(--text-muted)',
              textDecoration: 'none',
            }}
          >
            <Radio size={18} />
            Live Tail
          </NavLink>
          <NavLink
            to="/service-map"
            onClick={() => setMoreOpen(false)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.625rem',
              width: '100%',
              padding: '0.625rem 0.75rem',
              borderRadius: '0.5rem',
              fontSize: '0.875rem',
              fontWeight: 500,
              color: 'var(--text-muted)',
              textDecoration: 'none',
            }}
          >
            <Network size={18} />
            Service Map
          </NavLink>
          <NavLink
            to="/database"
            onClick={() => setMoreOpen(false)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.625rem',
              width: '100%',
              padding: '0.625rem 0.75rem',
              borderRadius: '0.5rem',
              fontSize: '0.875rem',
              fontWeight: 500,
              color: 'var(--text-muted)',
              textDecoration: 'none',
            }}
          >
            <Database size={18} />
            Database
          </NavLink>
          <NavLink
            to="/pages"
            onClick={() => setMoreOpen(false)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.625rem',
              width: '100%',
              padding: '0.625rem 0.75rem',
              borderRadius: '0.5rem',
              fontSize: '0.875rem',
              fontWeight: 500,
              color: 'var(--text-muted)',
              textDecoration: 'none',
            }}
          >
            <Globe size={18} />
            Pages
          </NavLink>
          <NavLink
            to="/settings"
            onClick={() => setMoreOpen(false)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.625rem',
              width: '100%',
              padding: '0.625rem 0.75rem',
              borderRadius: '0.5rem',
              fontSize: '0.875rem',
              fontWeight: 500,
              color: 'var(--text-muted)',
              textDecoration: 'none',
            }}
          >
            <Settings size={18} />
            Settings
          </NavLink>
          <button
            onClick={() => {
              setMoreOpen(false)
              handleLogout()
            }}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.625rem',
              width: '100%',
              padding: '0.625rem 0.75rem',
              borderRadius: '0.5rem',
              fontSize: '0.875rem',
              fontWeight: 500,
              color: 'var(--text-muted)',
              background: 'transparent',
              border: 'none',
              cursor: 'pointer',
              textAlign: 'left',
            }}
          >
            <LogOut size={18} />
            Logout
          </button>
        </div>
      )}

      {/* Tab bar */}
      <nav
        className="mobile-tab-bar"
        style={{
          position: 'fixed',
          bottom: 0,
          left: 0,
          right: 0,
          background: 'rgba(26, 29, 39, 0.85)',
          backdropFilter: 'blur(12px)',
          WebkitBackdropFilter: 'blur(12px)',
          borderTop: '1px solid var(--border)',
          display: 'flex',
          justifyContent: 'space-around',
          alignItems: 'center',
          paddingBottom: 'env(safe-area-inset-bottom, 0px)',
          zIndex: 1000,
          height: 'calc(60px + env(safe-area-inset-bottom, 0px))',
        }}
      >
        {tabs.map(({ to, icon: Icon, label }) => (
          <NavLink
            key={to}
            to={to}
            end={to === '/'}
            style={({ isActive }) => ({
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              justifyContent: 'center',
              gap: '0.125rem',
              flex: 1,
              height: '60px',
              fontSize: '0.625rem',
              fontWeight: 500,
              color: isActive ? 'var(--accent)' : 'var(--text-muted)',
              textDecoration: 'none',
              transition: 'color 0.15s',
            })}
          >
            <Icon size={22} />
            {label}
          </NavLink>
        ))}
        <button
          onClick={() => setMoreOpen(!moreOpen)}
          style={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            justifyContent: 'center',
            gap: '0.125rem',
            flex: 1,
            height: '60px',
            fontSize: '0.625rem',
            fontWeight: 500,
            color: moreOpen ? 'var(--accent)' : 'var(--text-muted)',
            background: 'none',
            border: 'none',
            cursor: 'pointer',
            transition: 'color 0.15s',
          }}
        >
          <MoreHorizontal size={22} />
          More
        </button>
      </nav>
    </>
  )
}

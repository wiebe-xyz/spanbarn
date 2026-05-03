import { type ReactElement } from 'react'
import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { Activity, GitBranch, Search, LogOut } from 'lucide-react'
import { api } from '../api/client'

const navItems = [
  { to: '/', icon: Activity, label: 'Services' },
  { to: '/traces', icon: Search, label: 'Traces' },
  { to: '/dependencies', icon: GitBranch, label: 'Dependencies' },
]

export function DashboardLayout(): ReactElement {
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
    <div style={{ display: 'flex', minHeight: '100vh' }}>
      {/* Sidebar */}
      <aside
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

        {/* Logout */}
        <div style={{ padding: '0.75rem 0.5rem', borderTop: '1px solid var(--border)' }}>
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
      <main style={{ flex: 1, padding: '1.5rem 2rem', overflowY: 'auto' }}>
        <Outlet />
      </main>
    </div>
  )
}

import type { CSSProperties, ReactElement, ReactNode, Ref } from 'react'
import { NavLink } from 'react-router-dom'
import type { LucideIcon } from 'lucide-react'

const rowStyle: CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: '0.625rem',
  width: '100%',
  padding: '0.625rem 0.75rem',
  borderRadius: '0.5rem',
  fontSize: '0.875rem',
  fontWeight: 500,
  color: 'var(--text-muted)',
}

const linkStyle: CSSProperties = { ...rowStyle, textDecoration: 'none' }

const buttonStyle: CSSProperties = {
  ...rowStyle,
  background: 'transparent',
  border: 'none',
  cursor: 'pointer',
  textAlign: 'left',
}

// One row of the phone More menu that navigates.
export function MenuLink({ to, icon: Icon, label, onNavigate }: {
  to: string
  icon: LucideIcon
  label: string
  onNavigate: () => void
}): ReactElement {
  return (
    <NavLink to={to} onClick={onNavigate} style={linkStyle}>
      <Icon size={18} />
      {label}
    </NavLink>
  )
}

// One row of the phone More menu that runs an action (Account, Logout).
export function MenuButton({ onClick, buttonRef, children }: {
  onClick: () => void
  buttonRef?: Ref<HTMLButtonElement>
  children: ReactNode
}): ReactElement {
  return (
    <button ref={buttonRef} onClick={onClick} style={buttonStyle}>
      {children}
    </button>
  )
}

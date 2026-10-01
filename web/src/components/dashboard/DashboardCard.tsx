import { type ReactElement, type ReactNode } from 'react'
import { Info } from 'lucide-react'

type DashboardCardProps = {
  title: string
  /** Shown as a tooltip on the info icon. */
  info: string
  loading: boolean
  error: string | null
  empty: boolean
  children: ReactNode
}

/** Shared frame for a dashboard chart: title, info tooltip, and the loading, error and empty states. */
export function DashboardCard({ title, info, loading, error, empty, children }: DashboardCardProps): ReactElement {
  let body: ReactNode = children
  if (error) {
    body = <div role="alert" style={messageStyle('var(--error)')}>{error}</div>
  } else if (loading && empty) {
    body = <div className="skeleton" data-testid="card-skeleton" style={{ height: 200 }} />
  } else if (empty) {
    body = <div style={messageStyle('var(--text-muted)')}>No data in this range</div>
  }

  return (
    <section className="card dashboard-card" aria-label={title} style={{ minWidth: 0 }}>
      <header style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '0.75rem' }}>
        <h3 style={{ fontSize: '0.875rem', fontWeight: 600, margin: 0 }}>{title}</h3>
        <span title={info} aria-label={info} style={{ color: 'var(--text-muted)', display: 'inline-flex' }}>
          <Info size={16} aria-hidden="true" />
        </span>
      </header>
      {body}
    </section>
  )
}

function messageStyle(color: string): React.CSSProperties {
  return {
    height: 200,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    fontSize: '0.8125rem',
    color,
  }
}

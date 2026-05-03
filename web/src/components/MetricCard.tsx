import type { ReactElement } from 'react'

type MetricCardProps = {
  label: string
  value: string
  color?: string
}

export function MetricCard({ label, value, color }: MetricCardProps): ReactElement {
  return (
    <div className="card" style={{ textAlign: 'center', minWidth: 120 }}>
      <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>
        {label}
      </div>
      <div
        style={{
          fontSize: '1.5rem',
          fontWeight: 700,
          color: color ?? 'var(--text)',
          fontFamily: "'SF Mono', 'Fira Code', monospace",
        }}
      >
        {value}
      </div>
    </div>
  )
}

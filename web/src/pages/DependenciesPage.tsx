import type { ReactElement } from 'react'

/** Placeholder — dependencies / service map page. */
export function DependenciesPage(): ReactElement {
  return (
    <div>
      <h2 style={{ fontSize: '1.25rem', fontWeight: 700, marginBottom: '1rem' }}>Dependencies</h2>
      <div className="card" style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '3rem' }}>
        Service dependency map coming soon.
      </div>
    </div>
  )
}

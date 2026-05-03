import type { ReactElement } from 'react'
import { useParams } from 'react-router-dom'

/** Placeholder for T11 — trace detail / waterfall page. */
export function TraceDetailPage(): ReactElement {
  const { traceId } = useParams<{ traceId: string }>()

  return (
    <div>
      <h2 style={{ fontSize: '1.25rem', fontWeight: 700, marginBottom: '1rem' }}>
        Trace {traceId?.slice(0, 16)}...
      </h2>
      <div className="card" style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '3rem' }}>
        Trace detail / waterfall coming in T11.
      </div>
    </div>
  )
}

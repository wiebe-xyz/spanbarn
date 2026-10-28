import { type ReactElement } from 'react'
import type { DataSource } from '../api/dashboardTypes'

const labels: Record<DataSource, { text: string; title: string }> = {
  rollups: {
    text: 'received spans',
    title: 'Counted from rollups of every span the server received, sample weights included. Spans dropped by sampling still count.',
  },
  spans: {
    text: 'stored spans',
    title: 'Counted from the spans the server stored. Spans dropped by sampling are not counted.',
  },
  aggregates: {
    text: 'estimated spans',
    title: 'Counted from per-minute aggregates, scaled up by the sample rate of each operation.',
  },
}

/** Names the store a count came from: rollups count received spans, raw spans count stored ones, aggregates estimate received ones. */
export function SourceLabel({ source }: { source?: DataSource }): ReactElement | null {
  if (!source) return null
  const l = labels[source]
  return (
    <span data-testid="source-label" title={l.title} style={{ color: 'var(--text-muted)', fontSize: '0.75rem', fontWeight: 400 }}>
      {l.text}
    </span>
  )
}

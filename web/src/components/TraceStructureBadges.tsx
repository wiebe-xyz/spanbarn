import type { CSSProperties, ReactElement } from 'react'
import type { TraceSummary } from '../api/types'

const badgeStyle: CSSProperties = {
  display: 'inline-block',
  borderRadius: 10,
  padding: '1px 8px',
  fontSize: 11,
  fontWeight: 500,
  whiteSpace: 'nowrap',
  border: '1px solid',
}

type Props = {
  trace: Pick<TraceSummary, 'hasRoot' | 'orphanCount'>
}

/**
 * Flags the structural defects of a trace in the list: no root span, and spans
 * whose parent is missing. Renders nothing for a healthy trace, and nothing
 * while the structure is not computed yet (hasRoot null).
 */
export function TraceStructureBadges({ trace }: Props): ReactElement | null {
  const orphans = trace.orphanCount ?? 0
  if (trace.hasRoot !== false && orphans === 0) return null
  return (
    <span style={{ display: 'inline-flex', gap: 6 }}>
      {trace.hasRoot === false && (
        <span
          style={{ ...badgeStyle, color: '#fbbf24', borderColor: 'rgba(251,191,36,0.4)', background: 'rgba(251,191,36,0.1)' }}
          title="No span without a parent was stored for this trace"
        >
          no root span
        </span>
      )}
      {orphans > 0 && (
        <span
          style={{ ...badgeStyle, color: '#93c5fd', borderColor: 'rgba(147,197,253,0.4)', background: 'rgba(147,197,253,0.1)' }}
          title="Spans whose parent span was never ingested"
        >
          {orphans} orphan {orphans === 1 ? 'span' : 'spans'}
        </span>
      )}
    </span>
  )
}

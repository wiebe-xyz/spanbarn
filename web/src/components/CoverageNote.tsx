import type { ReactElement } from 'react'
import { coverageNote } from '../utils/coverage'

/**
 * States the window a view read from the range's newest spans covers. Renders
 * nothing when the view read the whole range.
 */
export function CoverageNote({
  coveredFrom,
  rangeEnd,
  subject,
}: {
  coveredFrom?: string
  rangeEnd: Date | null
  subject?: string
}): ReactElement | null {
  if (!coveredFrom || !rangeEnd) return null
  return (
    <p role="note" style={{ marginBottom: '0.75rem', color: 'var(--text-muted)', fontSize: '0.875rem' }}>
      {coverageNote(coveredFrom, rangeEnd, subject)}
    </p>
  )
}

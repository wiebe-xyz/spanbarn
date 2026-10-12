import type { CSSProperties, ReactElement } from 'react'
import { formatCount, formatMicros, type ToolSpanStat } from '../utils/claudeCode'

const cell: CSSProperties = { padding: '0.5rem 0.75rem', textAlign: 'right', whiteSpace: 'nowrap' }

/** Tool calls and the time sessions spent blocked on the user, per tool, from spans. */
export function ToolSpanTable({ tools }: { tools: ToolSpanStat[] }): ReactElement | null {
  if (tools.length === 0) return null
  return (
    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: '0.5rem', overflow: 'auto', marginBottom: '1rem' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.875rem' }}>
        <thead>
          <tr style={{ borderBottom: '1px solid var(--border)' }}>
            <th style={{ ...cell, textAlign: 'left' }}>Tool</th>
            <th style={cell}>Calls</th>
            <th style={cell}>Blocked on user</th>
            <th style={cell}>Median wait</th>
            <th style={cell}>p95 wait</th>
          </tr>
        </thead>
        <tbody>
          {tools.map((t) => (
            <tr key={t.name} style={{ borderBottom: '1px solid var(--border)' }}>
              <td style={{ ...cell, textAlign: 'left', fontFamily: 'monospace' }}>{t.name}</td>
              <td style={cell}>{formatCount(t.calls)}</td>
              <td style={cell}>{formatCount(t.blocked)}</td>
              <td style={cell}>{t.blocked > 0 ? formatMicros(t.blockedP50Us) : '-'}</td>
              <td style={cell}>{t.blocked > 0 ? formatMicros(t.blockedP95Us) : '-'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

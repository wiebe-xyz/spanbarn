import type { CSSProperties, ReactElement } from 'react'
import type { AttributeComparison, AttributeDifference, AttributeValueShare } from '../../api/types'

function pct(share: number): string {
  return `${Math.round(share * 1000) / 10}%`
}

function scanText(label: string, scanned: number, truncated: boolean, max: number): string {
  const spans = `${scanned.toLocaleString()} ${scanned === 1 ? 'span' : 'spans'}`
  return `${label}: ${spans}${truncated ? ` (stopped at ${max.toLocaleString()}, newest only)` : ''}`
}

/** The ranked attributes of a comparison, each with the values that differ most. */
export function CompareResults({ data }: { data: AttributeComparison }): ReactElement {
  const empty = data.selection.scanned === 0
  return (
    <>
      <p style={{ fontSize: 13, color: '#9ca3af' }} data-testid="compare-note">
        {scanText('Selection', data.selection.scanned, data.selection.truncated, data.maxSpans)}.{' '}
        {scanText('Baseline', data.baseline.scanned, data.baseline.truncated, data.maxSpans)}.{' '}
        {data.sample > 1 ? `1 in ${data.sample} sample.` : 'Exact scan.'}
      </p>
      {empty && <p style={{ color: '#6b7280' }}>No spans match the selection in this range.</p>}
      {!empty && data.attributes.length === 0 && (
        <p style={{ color: '#6b7280' }}>No attribute differs between the selection and the baseline.</p>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {data.attributes.map((a) => <AttributeCard key={a.key} attr={a} />)}
      </div>
    </>
  )
}

function AttributeCard({ attr }: { attr: AttributeDifference }): ReactElement {
  return (
    <section style={cardStyle} aria-label={`Attribute ${attr.key}`}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
        <code style={{ fontSize: 13, color: '#93c5fd' }}>{attr.key}</code>
        <span style={{ fontSize: 12, color: '#9ca3af' }} title="Total variation distance, 0 (same) to 1 (nothing in common)">
          difference <strong style={{ color: '#e5e7eb' }}>{attr.score.toFixed(2)}</strong>
        </span>
        <span style={{ fontSize: 12, color: '#6b7280' }}>
          set on {pct(attr.selectionCoverage)} of selection, {pct(attr.baselineCoverage)} of baseline
        </span>
      </header>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12, marginTop: 8 }}>
        <thead>
          <tr>
            {['Value', 'Selection', 'Baseline'].map((h) => <th key={h} style={thStyle}>{h}</th>)}
          </tr>
        </thead>
        <tbody>
          {attr.values.map((v) => <ValueRow key={v.missing ? '\u0000missing' : v.value} v={v} />)}
        </tbody>
      </table>
    </section>
  )
}

function ValueRow({ v }: { v: AttributeValueShare }): ReactElement {
  const label = v.missing ? '(not set)' : v.value === '' ? '(empty)' : v.value
  return (
    <tr style={{ borderTop: '1px solid #1f2937' }}>
      <td style={{ ...tdStyle, fontFamily: 'monospace', wordBreak: 'break-all' }}>{label}</td>
      <td style={tdStyle}><ShareBar share={v.selectionShare} count={v.selectionCount} color="#f59e0b" /></td>
      <td style={tdStyle}><ShareBar share={v.baselineShare} count={v.baselineCount} color="#3b82f6" /></td>
    </tr>
  )
}

function ShareBar({ share, count, color }: { share: number; count: number; color: string }): ReactElement {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 140 }} title={`${count.toLocaleString()} spans`}>
      <div style={{ flex: 1, height: 6, background: '#1f2937', borderRadius: 3 }}>
        <div style={{ width: pct(share), height: 6, background: color, borderRadius: 3 }} />
      </div>
      <span style={{ width: 48, textAlign: 'right' }}>{pct(share)}</span>
    </div>
  )
}

const cardStyle: CSSProperties = { border: '1px solid #374151', borderRadius: 6, padding: '10px 12px', overflowX: 'auto' }

const thStyle: CSSProperties = {
  textAlign: 'left',
  padding: '4px 8px',
  color: '#9ca3af',
  fontWeight: 500,
  fontSize: 11,
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
}

const tdStyle: CSSProperties = { padding: '4px 8px', color: '#e5e7eb', verticalAlign: 'middle' }

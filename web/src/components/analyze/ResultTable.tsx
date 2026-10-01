import type { CSSProperties, ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import type { AnalyzeResponse, AnalyzeRow } from '../../api/types'
import { calcLabel, drillHref, formatCalc, valueLabel } from '../../analyze/model'
import { formatCount } from '../../utils/format'

type Props = {
  result: AnalyzeResponse
  projectId: number
  fromMs: number
  toMs: number
  orderBy: string
  asc: boolean
  onSort: (calc: string) => void
}

/** One row per group with a column per calculation. A group row opens the matching traces. */
export function ResultTable({ result, projectId, fromMs, toMs, orderBy, asc, onSort }: Props): ReactElement {
  const navigate = useNavigate()
  const sorted = orderBy || result.calcs[0]
  const rows = result.other ? [...result.rows, result.other] : result.rows
  const open = (row: AnalyzeRow) => {
    if (row.drill) navigate(drillHref(projectId, row.drill, fromMs, toMs + 60_000))
  }
  const keyCols = result.groupBy.length > 0 ? result.groupBy : ['Group']

  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ borderBottom: '1px solid #374151' }}>
            {keyCols.map((k) => <th key={k} style={thStyle}>{k}</th>)}
            {result.calcs.map((c) => (
              <th
                key={c}
                style={{ ...thStyle, textAlign: 'right' }}
                aria-sort={c === sorted ? (asc ? 'ascending' : 'descending') : 'none'}
              >
                <button type="button" onClick={() => onSort(c)} style={sortButton}>
                  {calcLabel(c)}
                  {c === sorted ? (asc ? ' ▲' : ' ▼') : ''}
                </button>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && (
            <tr>
              <td colSpan={keyCols.length + result.calcs.length} style={{ ...tdStyle, textAlign: 'center', color: '#6b7280' }}>
                No spans match in this range.
              </td>
            </tr>
          )}
          {rows.map((row, i) => (
            <tr
              key={row.other ? 'other' : i}
              data-testid={row.other ? 'other-row' : 'group-row'}
              onClick={() => open(row)}
              onKeyDown={(e) => { if (e.key === 'Enter') open(row) }}
              tabIndex={row.drill ? 0 : undefined}
              role={row.drill ? 'link' : undefined}
              aria-label={row.drill ? `Open traces for ${row.group.map(valueLabel).join(' / ')}` : undefined}
              style={{ borderBottom: '1px solid #1f2937', cursor: row.drill ? 'pointer' : 'default' }}
            >
              <KeyCells row={row} width={keyCols.length} />
              {result.calcs.map((c, j) => (
                <td key={c} style={{ ...tdStyle, textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>
                  {formatCalc(c, row.values[j])}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <p style={{ fontSize: 12, color: '#6b7280', margin: '8px 0 0' }}>
        {formatCount(result.scanned)} spans read.
      </p>
    </div>
  )
}

function KeyCells({ row, width }: { row: AnalyzeRow; width: number }): ReactElement {
  if (row.other) {
    return (
      <td colSpan={width} style={{ ...tdStyle, color: '#9ca3af', fontStyle: 'italic' }}>
        other ({formatCount(row.count)} spans in the remaining groups)
      </td>
    )
  }
  if (row.group.length === 0) return <td style={tdStyle}>all spans</td>
  return (
    <>
      {row.group.map((v, i) => (
        <td key={i} style={{ ...tdStyle, color: v === '' ? '#9ca3af' : '#e5e7eb', wordBreak: 'break-all' }}>
          {valueLabel(v)}
        </td>
      ))}
    </>
  )
}

const thStyle: CSSProperties = {
  textAlign: 'left',
  padding: '8px 12px',
  color: '#9ca3af',
  fontWeight: 500,
  fontSize: 11,
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
}
const tdStyle: CSSProperties = { padding: '8px 12px', color: '#e5e7eb', verticalAlign: 'top' }
const sortButton: CSSProperties = {
  background: 'transparent',
  border: 'none',
  color: 'inherit',
  font: 'inherit',
  textTransform: 'inherit',
  letterSpacing: 'inherit',
  cursor: 'pointer',
  padding: 0,
}

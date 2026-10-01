import { useState, type ReactElement } from 'react'
import type { CSSProperties } from 'react'
import { FilterBuilder } from '../filter/FilterBuilder'
import { inputStyle } from '../filter/styles'
import { useAttributeSuggestions } from '../../filters/useAttributeSuggestions'
import {
  FIXED_CALCS,
  LIMITS,
  MAX_GROUP_BY,
  RANGES,
  SAMPLES,
  distinctWire,
  isDistinct,
  type QueryState,
  type RangeValue,
} from '../../analyze/model'

type Project = { id: number; name: string }

type Props = {
  value: QueryState
  projects: Project[]
  onChange: (next: QueryState) => void
  onRun: () => void
  running: boolean
}

const KEY_LIST_ID = 'analyze-key-suggestions'

/** The inputs of a group-by query: project, range, filter, group by keys and calculations. */
export function QueryEditor({ value, projects, onChange, onRun, running }: Props): ReactElement {
  const suggestions = useAttributeSuggestions(value.projectId)
  const set = (changes: Partial<QueryState>) => onChange({ ...value, ...changes })

  const distinctKey = value.calcs.find(isDistinct)?.slice('count_distinct:'.length) ?? ''
  const [distinctDraft, setDistinctDraft] = useState(distinctKey)

  const toggleCalc = (wire: string) =>
    set({ calcs: value.calcs.includes(wire) ? value.calcs.filter((c) => c !== wire) : [...value.calcs, wire] })

  const setDistinct = (key: string) => {
    setDistinctDraft(key)
    const rest = value.calcs.filter((c) => !isDistinct(c))
    set({ calcs: key.trim() ? [...rest, distinctWire(key.trim())] : rest })
  }

  const setGroup = (i: number, key: string) => set({ groupBy: value.groupBy.map((g, j) => (j === i ? key : g)) })

  return (
    <form
      aria-label="Query"
      onSubmit={(e) => {
        e.preventDefault()
        onRun()
      }}
      style={{ marginBottom: 16 }}
    >
      <datalist id={KEY_LIST_ID}>
        {suggestions.keys.map((k) => <option key={k} value={k} />)}
      </datalist>

      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginBottom: 12 }}>
        <select
          aria-label="Project"
          value={value.projectId}
          onChange={(e) => set({ projectId: Number(e.target.value) })}
          style={inputStyle}
        >
          {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        <select
          aria-label="Time range"
          value={value.range}
          onChange={(e) => set({ range: e.target.value as RangeValue })}
          style={inputStyle}
        >
          {RANGES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
        <select
          aria-label="Group limit"
          value={value.limit}
          onChange={(e) => set({ limit: Number(e.target.value) })}
          style={inputStyle}
        >
          {LIMITS.map((n) => <option key={n} value={n}>Top {n} groups</option>)}
        </select>
        <select
          aria-label="Sampling"
          value={value.sample}
          onChange={(e) => set({ sample: e.target.value })}
          style={inputStyle}
        >
          {SAMPLES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
        </select>
      </div>

      <FilterBuilder value={value.filter} onChange={(filter) => set({ filter })} projectId={value.projectId} />

      <fieldset style={boxStyle} aria-label="Group by">
        <legend style={legendStyle}>Group by</legend>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {value.groupBy.map((g, i) => (
            <div key={i} style={{ display: 'flex', gap: 8 }}>
              <input
                aria-label={`Group by key ${i + 1}`}
                list={KEY_LIST_ID}
                value={g}
                placeholder="column or attribute, for example url.path"
                onChange={(e) => setGroup(i, e.target.value)}
                style={{ ...inputStyle, flex: 1, minWidth: 0 }}
              />
              <button
                type="button"
                aria-label={`Remove group by key ${i + 1}`}
                onClick={() => set({ groupBy: value.groupBy.filter((_, j) => j !== i) })}
                style={linkButton}
              >
                Remove
              </button>
            </div>
          ))}
        </div>
        {value.groupBy.length < MAX_GROUP_BY && (
          <button
            type="button"
            onClick={() => set({ groupBy: [...value.groupBy, ''] })}
            style={{ ...linkButton, marginTop: value.groupBy.length > 0 ? 8 : 0 }}
          >
            + Add group by
          </button>
        )}
      </fieldset>

      <fieldset style={boxStyle} aria-label="Calculations">
        <legend style={legendStyle}>Calculations</legend>
        <div style={{ display: 'flex', gap: '8px 16px', flexWrap: 'wrap', alignItems: 'center' }}>
          {FIXED_CALCS.map((c) => (
            <label key={c.wire} style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 13 }}>
              <input type="checkbox" checked={value.calcs.includes(c.wire)} onChange={() => toggleCalc(c.wire)} />
              {c.label}
            </label>
          ))}
          <label style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 13 }}>
            Distinct values of
            <input
              aria-label="Distinct values of"
              list={KEY_LIST_ID}
              value={distinctDraft}
              placeholder="attribute"
              onChange={(e) => setDistinct(e.target.value)}
              style={{ ...inputStyle, width: 180 }}
            />
          </label>
        </div>
      </fieldset>

      <button type="submit" disabled={running} style={runButton}>
        {running ? 'Running...' : 'Run query'}
      </button>
    </form>
  )
}

const boxStyle: CSSProperties = { border: '1px solid #374151', borderRadius: 6, padding: '8px 12px', margin: '0 0 12px' }
const legendStyle: CSSProperties = { fontSize: 12, color: '#9ca3af', padding: '0 6px' }
const linkButton: CSSProperties = {
  background: 'transparent',
  border: 'none',
  color: '#60a5fa',
  fontSize: 12,
  cursor: 'pointer',
  padding: 0,
}
const runButton: CSSProperties = {
  background: '#2563eb',
  border: 'none',
  borderRadius: 6,
  color: '#fff',
  padding: '8px 16px',
  fontSize: 13,
  cursor: 'pointer',
}

import type { CSSProperties, ReactElement } from 'react'
import {
  emptyCondition,
  isGroup,
  type Condition,
  type FilterExpr,
  type FilterNode,
  type Group,
  type Match,
} from '../../filters/model'
import { useAttributeSuggestions } from '../../filters/useAttributeSuggestions'
import { ConditionRow } from './ConditionRow'
import { inputStyle } from './styles'

type Props = {
  value: FilterExpr
  onChange: (next: FilterExpr) => void
  /** Project whose attribute keys and values are suggested. 0 turns suggestions off. */
  projectId: number
}

function MatchSelect({ value, onChange, label }: { value: Match; onChange: (m: Match) => void; label: string }): ReactElement {
  return (
    <select aria-label={label} value={value} onChange={(e) => onChange(e.target.value as Match)} style={inputStyle}>
      <option value="and">all of (AND)</option>
      <option value="or">any of (OR)</option>
    </select>
  )
}

/**
 * Builds the shared filter model: rows of key, operator and value joined by AND
 * or OR, with one level of groups.
 */
export function FilterBuilder({ value, onChange, projectId }: Props): ReactElement {
  const suggestions = useAttributeSuggestions(projectId)

  const setNode = (i: number, node: FilterNode) =>
    onChange({ ...value, filters: value.filters.map((n, j) => (j === i ? node : n)) })
  const removeNode = (i: number) => onChange({ ...value, filters: value.filters.filter((_, j) => j !== i) })
  const addCondition = () => onChange({ ...value, filters: [...value.filters, emptyCondition()] })
  const addGroup = () =>
    onChange({ ...value, filters: [...value.filters, { match: 'or', filters: [emptyCondition(), emptyCondition()] }] })

  const setGroupRow = (g: Group, gi: number, ci: number, c: Condition) =>
    setNode(gi, { ...g, filters: g.filters.map((x, k) => (k === ci ? c : x)) })
  const removeGroupRow = (g: Group, gi: number, ci: number) => {
    const rest = g.filters.filter((_, k) => k !== ci)
    if (rest.length === 0) removeNode(gi)
    else setNode(gi, { ...g, filters: rest })
  }

  return (
    <fieldset style={boxStyle} aria-label="Attribute filters">
      <legend style={{ fontSize: 12, color: '#9ca3af', padding: '0 6px' }}>Attribute filters</legend>
      {value.filters.length > 1 && (
        <div style={{ marginBottom: 8, fontSize: 12, color: '#9ca3af', display: 'flex', gap: 8, alignItems: 'center' }}>
          Match <MatchSelect label="Match" value={value.match} onChange={(match) => onChange({ ...value, match })} />
        </div>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {value.filters.map((node, i) =>
          isGroup(node) ? (
            <div key={i} style={groupStyle} role="group" aria-label="Filter group">
              <div style={{ fontSize: 12, color: '#9ca3af', display: 'flex', gap: 8, alignItems: 'center' }}>
                Group, match <MatchSelect label="Group match" value={node.match} onChange={(match) => setNode(i, { ...node, match })} />
                <button type="button" onClick={() => removeNode(i)} style={linkButton}>Remove group</button>
              </div>
              {node.filters.map((c, ci) => (
                <ConditionRow
                  key={ci}
                  condition={c}
                  suggestions={suggestions}
                  onChange={(next) => setGroupRow(node, i, ci, next)}
                  onRemove={() => removeGroupRow(node, i, ci)}
                />
              ))}
              <div>
                <button
                  type="button"
                  onClick={() => setNode(i, { ...node, filters: [...node.filters, emptyCondition()] })}
                  style={linkButton}
                >
                  + Add to group
                </button>
              </div>
            </div>
          ) : (
            <ConditionRow
              key={i}
              condition={node}
              suggestions={suggestions}
              onChange={(next) => setNode(i, next)}
              onRemove={() => removeNode(i)}
            />
          ),
        )}
      </div>
      <div style={{ display: 'flex', gap: 12, marginTop: value.filters.length > 0 ? 8 : 0 }}>
        <button type="button" onClick={addCondition} style={linkButton}>+ Add filter</button>
        <button type="button" onClick={addGroup} style={linkButton}>+ Add group</button>
      </div>
    </fieldset>
  )
}

const boxStyle: CSSProperties = {
  border: '1px solid #374151',
  borderRadius: 6,
  padding: '8px 12px',
  margin: '0 0 12px',
}

const groupStyle: CSSProperties = {
  border: '1px dashed #4b5563',
  borderRadius: 6,
  padding: 8,
  display: 'flex',
  flexDirection: 'column',
  gap: 8,
}

const linkButton: CSSProperties = {
  background: 'transparent',
  border: 'none',
  color: '#60a5fa',
  fontSize: 12,
  cursor: 'pointer',
  padding: 0,
}

import type { CSSProperties, ReactElement } from 'react'
import { useId, useState } from 'react'
import { OPERATORS, takesList, takesNoValue, type Condition, type FilterOp } from '../../filters/model'
import type { Suggestions } from '../../filters/useAttributeSuggestions'
import { inputStyle } from './styles'

type Props = {
  condition: Condition
  suggestions: Suggestions
  onChange: (c: Condition) => void
  onRemove: () => void
}

/** One filter row: key, operator, value and a remove button. */
export function ConditionRow({ condition, suggestions, onChange, onRemove }: Props): ReactElement {
  const keyListId = useId()
  const valueListId = useId()
  const [values, setValues] = useState<string[]>([])

  const setOp = (op: FilterOp) => {
    const next: Condition = { key: condition.key, op }
    if (takesList(op)) next.values = condition.values ?? (condition.value ? [condition.value] : [])
    else if (!takesNoValue(op)) next.value = condition.value ?? condition.values?.[0] ?? ''
    onChange(next)
  }

  const listText = (condition.values ?? []).join(',')

  return (
    <div style={rowStyle} data-testid="filter-row">
      <input
        aria-label="Filter key"
        list={keyListId}
        placeholder="key, e.g. url.path"
        value={condition.key}
        onChange={(e) => onChange({ ...condition, key: e.target.value })}
        style={{ ...inputStyle, minWidth: 180, flex: '1 1 180px' }}
      />
      <datalist id={keyListId}>
        {suggestions.keys.map((k) => <option key={k} value={k} />)}
      </datalist>

      <select
        aria-label="Filter operator"
        value={condition.op}
        onChange={(e) => setOp(e.target.value as FilterOp)}
        style={inputStyle}
      >
        {OPERATORS.map((o) => <option key={o.op} value={o.op}>{o.label}</option>)}
      </select>

      {!takesNoValue(condition.op) && (
        <>
          <input
            aria-label="Filter value"
            list={valueListId}
            placeholder={takesList(condition.op) ? 'a, b, c' : 'value'}
            value={takesList(condition.op) ? listText : condition.value ?? ''}
            onFocus={() => { void suggestions.loadValues(condition.key).then(setValues) }}
            onChange={(e) =>
              onChange(
                takesList(condition.op)
                  ? { ...condition, values: e.target.value.split(',').map((v) => v.trim()) }
                  : { ...condition, value: e.target.value },
              )
            }
            style={{ ...inputStyle, minWidth: 160, flex: '1 1 160px' }}
          />
          {!takesList(condition.op) && (
            <datalist id={valueListId}>
              {values.map((v) => <option key={v} value={v} />)}
            </datalist>
          )}
        </>
      )}

      <button type="button" aria-label="Remove filter" onClick={onRemove} style={removeStyle} title="Remove">
        &times;
      </button>
    </div>
  )
}

const rowStyle: CSSProperties = { display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }

const removeStyle: CSSProperties = {
  background: 'transparent',
  border: 'none',
  color: '#6b7280',
  fontSize: 18,
  lineHeight: 1,
  cursor: 'pointer',
  padding: '0 6px',
}

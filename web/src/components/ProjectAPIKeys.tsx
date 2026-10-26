import { useState, useEffect, useCallback, type ReactElement } from 'react'
import { Key, ChevronDown, ChevronRight } from 'lucide-react'
import { fetchJSON } from '../api/client'

type APIKey = {
  id: number
  projectId: number
  name: string
  scope: string
  lastUsedAt: string | null
  createdAt: string
}

type CreatedAPIKey = APIKey & { key: string }

const fieldStyle = { padding: '0.3rem 0.5rem', fontSize: '0.75rem' } as const

function ScopeBadge({ scope }: { scope: string }): ReactElement {
  const ingest = scope === 'ingest'
  return (
    <span
      style={{
        fontSize: 10,
        fontWeight: 700,
        background: ingest ? 'rgba(59,130,246,0.1)' : 'rgba(168,85,247,0.1)',
        color: ingest ? '#3b82f6' : '#a855f7',
        border: `1px solid ${ingest ? 'rgba(59,130,246,0.3)' : 'rgba(168,85,247,0.3)'}`,
        borderRadius: 99,
        padding: '0.05rem 0.4rem',
        textTransform: 'uppercase',
      }}
    >
      {scope}
    </span>
  )
}

// The key itself comes back once, from the create call. The list only ever
// shows names and scopes.
function NewKeyNotice({ created, onDismiss }: { created: CreatedAPIKey; onDismiss: () => void }): ReactElement {
  return (
    <div
      role="status"
      style={{
        border: '1px solid var(--border)',
        borderRadius: 6,
        padding: '0.5rem',
        fontSize: '0.75rem',
        display: 'flex',
        flexDirection: 'column',
        gap: 6,
      }}
    >
      <span>
        Key <strong>{created.name}</strong> created. Copy it now: it is not shown again.
      </span>
      <input type="text" readOnly value={created.key} aria-label="New API key" onFocus={(e) => e.target.select()} style={fieldStyle} />
      <button type="button" className="btn btn-sm" onClick={onDismiss} style={{ alignSelf: 'flex-start' }}>
        Done
      </button>
    </div>
  )
}

export function ProjectAPIKeys({ projectId }: { projectId: number }): ReactElement {
  const [keys, setKeys] = useState<APIKey[] | null>(null)
  const [expanded, setExpanded] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [scope, setScope] = useState<'ingest' | 'read'>('ingest')
  const [busy, setBusy] = useState(false)
  const [created, setCreated] = useState<CreatedAPIKey | null>(null)
  const [confirmRevoke, setConfirmRevoke] = useState<number | null>(null)

  const base = `/api/v1/projects/${projectId}/apikeys`

  const fetchKeys = useCallback(async () => {
    try {
      const data = await fetchJSON<APIKey[]>(base)
      setKeys(data ?? [])
    } catch (e) {
      setError(String(e))
      setKeys([])
    }
  }, [base])

  useEffect(() => {
    if (expanded && keys === null) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
      void fetchKeys()
    }
  }, [expanded, keys, fetchKeys])

  const run = async (action: () => Promise<void>) => {
    setBusy(true)
    setError(null)
    try {
      await action()
      await fetchKeys()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const create = () =>
    run(async () => {
      const key = await fetchJSON<CreatedAPIKey>(base, { method: 'POST', body: JSON.stringify({ name: name.trim(), scope }) })
      setCreated(key)
      setName('')
    })

  const revoke = (id: number) =>
    run(async () => {
      await fetchJSON<void>(`${base}/${id}`, { method: 'DELETE' })
      setConfirmRevoke(null)
    })

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 4,
          background: 'none',
          border: 'none',
          color: 'var(--accent)',
          cursor: 'pointer',
          fontSize: '0.8125rem',
          fontFamily: 'inherit',
          padding: 0,
        }}
      >
        <Key size={12} />
        {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        API Keys
      </button>
      {expanded && (
        <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 8 }}>
          {keys === null ? (
            <div className="skeleton" style={{ height: 40 }} />
          ) : keys.length === 0 ? (
            <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', fontStyle: 'italic' }}>No API keys</div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              {keys.map((k) => (
                <div key={k.id} style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: '0.75rem', flexWrap: 'wrap' }}>
                  <span style={{ fontWeight: 500 }}>{k.name}</span>
                  <ScopeBadge scope={k.scope} />
                  {k.lastUsedAt && (
                    <span style={{ color: 'var(--text-muted)' }}>last used {new Date(k.lastUsedAt).toLocaleString()}</span>
                  )}
                  {confirmRevoke === k.id ? (
                    <>
                      <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void revoke(k.id)}>
                        Confirm revoke
                      </button>
                      <button type="button" className="btn btn-sm" onClick={() => setConfirmRevoke(null)}>
                        Cancel
                      </button>
                    </>
                  ) : (
                    <button type="button" className="btn btn-sm" disabled={busy} onClick={() => setConfirmRevoke(k.id)}>
                      Revoke
                    </button>
                  )}
                </div>
              ))}
            </div>
          )}
          {created && <NewKeyNotice created={created} onDismiss={() => setCreated(null)} />}
          <form
            onSubmit={(e) => {
              e.preventDefault()
              void create()
            }}
            style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}
          >
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="key name"
              aria-label="New key name"
              maxLength={64}
              style={{ ...fieldStyle, flex: 1, minWidth: 0 }}
            />
            <select value={scope} onChange={(e) => setScope(e.target.value as 'ingest' | 'read')} aria-label="New key scope" style={fieldStyle}>
              <option value="ingest">ingest</option>
              <option value="read">read</option>
            </select>
            <button type="submit" className="btn btn-sm" disabled={busy || name.trim() === ''}>
              Create key
            </button>
          </form>
          {error && <div style={{ fontSize: '0.75rem', color: 'var(--error, #ef4444)' }}>{error}</div>}
        </div>
      )}
    </div>
  )
}

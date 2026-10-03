import { useCallback, useEffect, useState, type FormEvent, type ReactElement } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { calculatedFieldsApi, type CalculatedField, type CalculatedFieldSample } from '../api/calculatedFields'
import { buttonStyle, errorStyle, inputStyle, mutedText, primaryButton, selectStyle } from '../components/boards/styles'

type Project = { id: number; name: string }

/** How long the editor waits after a keystroke before it asks for a preview. */
const PREVIEW_DELAY_MS = 300

function errorMessage(e: unknown, fallback: string): string {
  return e instanceof Error ? e.message : fallback
}

function formatValue(v: CalculatedFieldSample['value']): string {
  if (v === null || v === undefined) return 'empty'
  return String(v)
}

type EditorProps = {
  projectId: number
  /** The field being edited, or null for a new one. */
  editing: CalculatedField | null
  onSaved: () => void
  onCancel: () => void
}

/** Name and expression inputs with a live preview on the newest spans. */
function FieldEditor({ projectId, editing, onSaved, onCancel }: EditorProps): ReactElement {
  const [name, setName] = useState(editing?.name ?? '')
  const [expression, setExpression] = useState(editing?.expression ?? '')
  const [samples, setSamples] = useState<CalculatedFieldSample[] | null>(null)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const expr = expression.trim()
    if (!expr) return
    let cancelled = false
    const timer = setTimeout(() => {
      calculatedFieldsApi
        .preview(projectId, name.trim(), expr)
        .then((r) => {
          if (cancelled) return
          setSamples(r.samples ?? [])
          setPreviewError(null)
        })
        .catch((e: unknown) => {
          if (cancelled) return
          setSamples(null)
          setPreviewError(errorMessage(e, 'Could not preview the expression'))
        })
    }, PREVIEW_DELAY_MS)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [projectId, name, expression])

  const save = (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    const n = name.trim()
    const x = expression.trim()
    const done = editing
      ? calculatedFieldsApi.update(editing.id, n, x)
      : calculatedFieldsApi.create(projectId, n, x)
    done.then(onSaved).catch((err: unknown) => setError(errorMessage(err, 'Could not save the field')))
  }

  return (
    <form onSubmit={save} style={{ display: 'grid', gap: 8, maxWidth: 640, marginBottom: 24 }}>
      <h2 style={{ fontSize: 15, fontWeight: 600, margin: 0 }}>{editing ? `Edit ${editing.name}` : 'New calculated field'}</h2>
      <input
        aria-label="Field name"
        placeholder="Name, for example duration_ms"
        value={name}
        maxLength={64}
        onChange={(e) => setName(e.target.value)}
        style={inputStyle}
      />
      <textarea
        aria-label="Expression"
        placeholder="Expression, for example duration_us / 1000"
        value={expression}
        maxLength={500}
        rows={3}
        onChange={(e) => setExpression(e.target.value)}
        style={{ ...inputStyle, fontFamily: 'monospace', resize: 'vertical' }}
      />
      <p style={{ ...mutedText, margin: 0 }}>
        Columns and attributes by name (<code>duration_us</code>, <code>http.route</code>), numbers, 'text', <code>+ - * / %</code>,{' '}
        <code>= != &lt; &lt;= &gt; &gt;=</code>, <code>and or not</code>, and <code>coalesce</code>, <code>if</code>, <code>concat</code>,{' '}
        <code>lower</code>. Put other characters in backticks, for example <code>`app.user-id`</code>. Use <code>attributes.name</code> to
        read an attribute that a field of the same name replaces.
      </p>

      <div aria-label="Preview" style={{ background: '#111827', border: '1px solid #1f2937', borderRadius: 8, padding: 12 }}>
        <div style={{ ...mutedText, marginBottom: 6 }}>Preview on the newest spans</div>
        {!expression.trim() ? (
          <div style={mutedText}>Type an expression to see its value.</div>
        ) : previewError ? (
          <div role="alert" style={{ ...errorStyle, marginBottom: 0 }}>{previewError}</div>
        ) : samples === null ? (
          <div style={mutedText}>Type an expression to see its value.</div>
        ) : samples.length === 0 ? (
          <div style={mutedText}>This project has no spans yet.</div>
        ) : (
          <table style={{ fontSize: 13, borderCollapse: 'collapse', width: '100%' }}>
            <thead>
              <tr style={{ textAlign: 'left', color: '#9ca3af' }}>
                <th style={{ padding: '2px 8px 2px 0' }}>Span</th>
                <th style={{ padding: '2px 0' }}>Value</th>
              </tr>
            </thead>
            <tbody>
              {samples.map((s) => (
                <tr key={s.spanId}>
                  <td style={{ padding: '2px 8px 2px 0' }}>{s.name}</td>
                  <td style={{ padding: '2px 0', fontFamily: 'monospace' }}>{formatValue(s.value)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {error && <div role="alert" style={errorStyle}>{error}</div>}
      <div style={{ display: 'flex', gap: 8 }}>
        <button type="submit" style={primaryButton} disabled={!name.trim() || !expression.trim()}>Save field</button>
        <button type="button" style={buttonStyle} onClick={onCancel}>Cancel</button>
      </div>
    </form>
  )
}

/** The calculated fields of a project, with an editor that previews on real spans. */
export function CalculatedFieldsPage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const [projects, setProjects] = useState<Project[]>([])
  const [fields, setFields] = useState<CalculatedField[] | null>(null)
  const [editor, setEditor] = useState<{ editing: CalculatedField | null } | null>(null)
  const [error, setError] = useState<string | null>(null)

  const projectId = Number(params.get('project')) || projects[0]?.id || 0

  useEffect(() => {
    api.listProjects().then((p) => setProjects((p ?? []).map(({ id, name }) => ({ id, name })))).catch(() => {})
  }, [])

  const load = useCallback(() => {
    if (!projectId) return () => {}
    let cancelled = false
    calculatedFieldsApi
      .list(projectId)
      .then((f) => { if (!cancelled) setFields(f ?? []) })
      .catch((e: unknown) => { if (!cancelled) setError(errorMessage(e, 'Could not load the fields')) })
    return () => { cancelled = true }
  }, [projectId])

  useEffect(() => load(), [load])

  const remove = (f: CalculatedField) => {
    setError(null)
    calculatedFieldsApi
      .remove(f.id)
      .then(() => load())
      .catch((e: unknown) => setError(errorMessage(e, 'Could not delete the field')))
  }

  return (
    <div style={{ padding: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4, flexWrap: 'wrap' }}>
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>Calculated fields</h1>
        <Link to={`/attributes${projectId ? `?project=${projectId}` : ''}`} style={{ fontSize: 13, color: '#93c5fd' }}>Back to attributes</Link>
      </div>
      <p style={{ ...mutedText, margin: '0 0 16px' }}>
        A calculated field is a named expression over span columns and attributes. Use its name as a key in filters and group-bys, like any attribute.
      </p>

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', marginBottom: 16 }}>
        <select
          aria-label="Project"
          value={projectId}
          onChange={(e) => { setFields(null); setEditor(null); setParams({ project: e.target.value }) }}
          style={selectStyle}
        >
          {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        {!editor && (
          <button type="button" style={primaryButton} disabled={!projectId} onClick={() => setEditor({ editing: null })}>
            New field
          </button>
        )}
      </div>

      {error && <div role="alert" style={errorStyle}>{error}</div>}

      {editor && (
        <FieldEditor
          key={editor.editing?.id ?? 'new'}
          projectId={projectId}
          editing={editor.editing}
          onSaved={() => { setEditor(null); load() }}
          onCancel={() => setEditor(null)}
        />
      )}

      {fields === null ? (
        <p style={mutedText}>Loading fields...</p>
      ) : fields.length === 0 ? (
        <p style={mutedText}>No calculated fields in this project yet.</p>
      ) : (
        <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'grid', gap: 8, maxWidth: 640 }}>
          {fields.map((f) => (
            <li
              key={f.id}
              style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12, padding: '10px 14px', background: '#111827', border: '1px solid #1f2937', borderRadius: 8 }}
            >
              <div style={{ minWidth: 0 }}>
                <div style={{ fontWeight: 600 }}>{f.name}</div>
                <code style={{ ...mutedText, wordBreak: 'break-all' }}>{f.expression}</code>
              </div>
              <div style={{ display: 'flex', gap: 6 }}>
                <button type="button" style={buttonStyle} aria-label={`Edit ${f.name}`} onClick={() => setEditor({ editing: f })}>Edit</button>
                <button type="button" style={buttonStyle} aria-label={`Delete ${f.name}`} onClick={() => remove(f)}>Delete</button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

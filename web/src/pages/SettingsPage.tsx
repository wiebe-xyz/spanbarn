import { useState, useEffect, useCallback, type ReactElement } from 'react'
import { CheckCircle, XCircle, ExternalLink, Copy, Check, Key, ChevronDown, ChevronRight, Zap, HardDrive, MemoryStick } from 'lucide-react'
import { fetchJSON } from '../api/client'

type SystemStats = {
  db: {
    dbSizeBytes: number
    spanCount: number
    aggregateCount: number
    errorSampleCount: number
    spoolSizeBytes: number
  }
  memory: {
    allocBytes: number
    sysBytes: number
    numGC: number
  }
}

type RetentionSettings = {
  retention_full_hours: string
  retention_aggregated_days: string
  retention_error_days: string
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

function formatNumber(n: number): string {
  return n.toLocaleString()
}

function SystemHealthPanel() {
  const [stats, setStats] = useState<SystemStats | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetchJSON<SystemStats>('/api/v1/stats')
      .then(setStats)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div className="skeleton" style={{ height: 120 }} />
  if (!stats) return null

  return (
    <div className="card" style={{ marginBottom: '1.5rem' }}>
      <div style={{ fontWeight: 700, fontSize: '0.875rem', marginBottom: '0.75rem' }}>
        System Health
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: '1rem' }}>
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
            <HardDrive size={14} color="var(--accent)" />
            <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Database</span>
          </div>
          <div style={{ fontSize: '1.125rem', fontWeight: 700 }}>{formatBytes(stats.db.dbSizeBytes)}</div>
        </div>
        <div>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: 4 }}>Spool</div>
          <div style={{ fontSize: '1.125rem', fontWeight: 700 }}>{formatBytes(stats.db.spoolSizeBytes)}</div>
        </div>
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
            <MemoryStick size={14} color="var(--accent)" />
            <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Memory</span>
          </div>
          <div style={{ fontSize: '1.125rem', fontWeight: 700 }}>{formatBytes(stats.memory.allocBytes)}</div>
          <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)' }}>of {formatBytes(stats.memory.sysBytes)} sys</div>
        </div>
        <div>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: 4 }}>Spans</div>
          <div style={{ fontSize: '1.125rem', fontWeight: 700 }}>{formatNumber(stats.db.spanCount)}</div>
        </div>
        <div>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: 4 }}>Aggregates</div>
          <div style={{ fontSize: '1.125rem', fontWeight: 700 }}>{formatNumber(stats.db.aggregateCount)}</div>
        </div>
        <div>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: 4 }}>Error Samples</div>
          <div style={{ fontSize: '1.125rem', fontWeight: 700 }}>{formatNumber(stats.db.errorSampleCount)}</div>
        </div>
      </div>
    </div>
  )
}

function RetentionSettingsPanel() {
  const [settings, setSettings] = useState<RetentionSettings>({
    retention_full_hours: '72',
    retention_aggregated_days: '30',
    retention_error_days: '90',
  })
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetchJSON<Record<string, string>>('/api/v1/settings')
      .then((data) => {
        if (data) {
          setSettings((prev) => ({ ...prev, ...data }))
        }
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  const handleSave = async () => {
    setSaving(true)
    try {
      await fetchJSON('/api/v1/settings', {
        method: 'PUT',
        body: JSON.stringify(settings),
      })
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch {
      // ignore
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <div className="skeleton" style={{ height: 100 }} />

  const fieldStyle: React.CSSProperties = {
    padding: '0.375rem 0.5rem',
    fontSize: '0.8125rem',
    width: 80,
  }

  return (
    <div className="card" style={{ marginBottom: '1.5rem' }}>
      <div style={{ fontWeight: 700, fontSize: '0.875rem', marginBottom: '0.75rem' }}>
        Retention
      </div>
      <div style={{ display: 'flex', gap: '1.5rem', flexWrap: 'wrap', alignItems: 'end' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Span retention (hours)</span>
          <input
            type="number"
            min="1"
            value={settings.retention_full_hours}
            onChange={(e) => setSettings((s) => ({ ...s, retention_full_hours: e.target.value }))}
            style={fieldStyle}
          />
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Aggregate retention (days)</span>
          <input
            type="number"
            min="1"
            value={settings.retention_aggregated_days}
            onChange={(e) => setSettings((s) => ({ ...s, retention_aggregated_days: e.target.value }))}
            style={fieldStyle}
          />
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Error sample retention (days)</span>
          <input
            type="number"
            min="1"
            value={settings.retention_error_days}
            onChange={(e) => setSettings((s) => ({ ...s, retention_error_days: e.target.value }))}
            style={fieldStyle}
          />
        </label>
        <button
          onClick={handleSave}
          disabled={saving}
          className="btn"
          style={{ fontSize: '0.8125rem' }}
        >
          {saved ? 'Saved!' : saving ? 'Saving...' : 'Save'}
        </button>
      </div>
      <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginTop: 8 }}>
        Spans older than the retention window are aggregated and deleted. Changes take effect on the next retention cycle (~5 min).
      </div>
    </div>
  )
}

type Project = {
  id: number
  slug: string
  name: string
  status: string
  createdAt: string
}

type ProjectStats = {
  projectId: number
  spanCount: number
  errorCount: number
}

type APIKey = {
  id: number
  projectId: number
  name: string
  scope: string
  lastUsedAt: string | null
  createdAt: string
}

function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    await navigator.clipboard.writeText(value)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <button
      onClick={handleCopy}
      className="btn btn-sm"
      style={{ padding: '0.3rem 0.5rem' }}
      title="Copy to clipboard"
    >
      {copied ? <Check size={13} /> : <Copy size={13} />}
    </button>
  )
}

function ProjectAPIKeys({ projectId }: { projectId: number }) {
  const [keys, setKeys] = useState<APIKey[]>([])
  const [expanded, setExpanded] = useState(false)
  const [loading, setLoading] = useState(false)

  const fetchKeys = useCallback(async () => {
    setLoading(true)
    try {
      const data = await fetchJSON<APIKey[]>(`/api/v1/projects/${projectId}/apikeys`)
      setKeys(data ?? [])
    } catch {
      // ignore
    } finally {
      setLoading(false)
    }
  }, [projectId])

  useEffect(() => {
    if (expanded && keys.length === 0) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
      void fetchKeys()
    }
  }, [expanded, keys.length, fetchKeys])

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
        <div style={{ marginTop: 8 }}>
          {loading ? (
            <div className="skeleton" style={{ height: 40 }} />
          ) : keys.length === 0 ? (
            <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', fontStyle: 'italic' }}>
              No API keys
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              {keys.map((k) => (
                <div
                  key={k.id}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    fontSize: '0.75rem',
                    flexWrap: 'wrap',
                  }}
                >
                  <span style={{ fontWeight: 500 }}>{k.name}</span>
                  <span
                    style={{
                      fontSize: 10,
                      fontWeight: 700,
                      background: k.scope === 'ingest' ? 'rgba(59,130,246,0.1)' : 'rgba(168,85,247,0.1)',
                      color: k.scope === 'ingest' ? '#3b82f6' : '#a855f7',
                      border: `1px solid ${k.scope === 'ingest' ? 'rgba(59,130,246,0.3)' : 'rgba(168,85,247,0.3)'}`,
                      borderRadius: 99,
                      padding: '0.05rem 0.4rem',
                      textTransform: 'uppercase',
                    }}
                  >
                    {k.scope}
                  </span>
                  {k.lastUsedAt && (
                    <span style={{ color: 'var(--text-muted)' }}>
                      last used {new Date(k.lastUsedAt).toLocaleDateString()}
                    </span>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function SettingsPage(): ReactElement {
  const [projects, setProjects] = useState<Project[]>([])
  const [statsMap, setStatsMap] = useState<Record<number, ProjectStats>>({})
  const [loading, setLoading] = useState(true)
  const [approvingProject, setApprovingProject] = useState<number | null>(null)
  const [rejectingProject, setRejectingProject] = useState<number | null>(null)
  const [error, setError] = useState('')

  const fetchProjects = useCallback(async () => {
    try {
      const [data, stats] = await Promise.all([
        fetchJSON<Project[]>('/api/v1/projects'),
        fetchJSON<ProjectStats[]>('/api/v1/projects/stats').catch(() => [] as ProjectStats[]),
      ])
      setProjects(data ?? [])
      const map: Record<number, ProjectStats> = {}
      for (const s of stats ?? []) map[s.projectId] = s
      setStatsMap(map)
    } catch {
      setError('Failed to load projects')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    void fetchProjects()
  }, [fetchProjects])

  const handleApprove = async (id: number) => {
    setApprovingProject(id)
    try {
      await fetchJSON<Project>(`/api/v1/projects/${id}/approve`, { method: 'POST' })
      await fetchProjects()
    } catch (e) {
      setError(String(e))
    } finally {
      setApprovingProject(null)
    }
  }

  const handleReject = async (id: number) => {
    setRejectingProject(id)
    try {
      await fetchJSON(`/api/v1/projects/${id}`, { method: 'DELETE' })
      await fetchProjects()
    } catch (e) {
      setError(String(e))
    } finally {
      setRejectingProject(null)
    }
  }

  const pendingProjects = projects.filter((p) => p.status === 'pending')
  const activeProjects = projects.filter((p) => p.status !== 'pending')

  if (loading) {
    return (
      <div>
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700, marginBottom: '1.5rem' }}>Settings</h2>
        <div className="skeleton" style={{ height: 200 }} />
      </div>
    )
  }

  return (
    <div>
      <h2 style={{ fontSize: '1.25rem', fontWeight: 700, marginBottom: '1.5rem' }}>Settings</h2>

      {error && (
        <div style={{ color: 'var(--error)', marginBottom: '1rem', fontSize: '0.875rem' }}>
          {error}
        </div>
      )}

      {/* System health + retention */}
      <SystemHealthPanel />
      <RetentionSettingsPanel />

      {/* LLM Setup reference */}
      <div
        className="card"
        style={{
          border: '1px solid rgba(59,130,246,0.3)',
          marginBottom: '1.5rem',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
          <Zap size={20} color="#3b82f6" style={{ flexShrink: 0, marginTop: 2 }} />
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 700, fontSize: '0.875rem', marginBottom: 4 }}>
              Quick Setup
            </div>
            <div style={{ fontSize: '0.8125rem', color: 'var(--text-muted)', marginBottom: 12 }}>
              Point an LLM or developer at the setup page to auto-configure a project with an ingest API key.
              The page returns a markdown guide with OTel integration examples.
            </div>
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                background: 'var(--bg)',
                border: '1px solid var(--border)',
                borderRadius: 8,
                padding: '0.5rem 0.75rem',
              }}
            >
              <code style={{ fontSize: '0.8125rem', color: 'var(--accent)', flex: 1, minWidth: 0, wordBreak: 'break-all' }}>
                {window.location.origin}/api/v1/setup/<strong>your-project-slug</strong>
              </code>
              <CopyButton value={`${window.location.origin}/api/v1/setup/your-project-slug`} />
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: 8 }}>
              Replace <code style={{ color: 'var(--text)' }}>your-project-slug</code> with the desired project name.
              The project will appear below as &quot;pending&quot; until you approve it.
            </div>
          </div>
        </div>
      </div>

      {/* Pending projects */}
      {pendingProjects.length > 0 && (
        <div
          className="card"
          style={{
            border: '1px solid rgba(245,158,11,0.4)',
            padding: 0,
            marginBottom: '1.5rem',
          }}
        >
          <div
            style={{
              padding: '1rem 1.25rem',
              borderBottom: '1px solid rgba(245,158,11,0.2)',
              background: 'rgba(245,158,11,0.06)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={{ fontWeight: 700, fontSize: '0.875rem' }}>Pending Projects</span>
              <span
                style={{
                  fontSize: 11,
                  fontWeight: 700,
                  background: 'rgba(245,158,11,0.15)',
                  color: '#f59e0b',
                  border: '1px solid rgba(245,158,11,0.35)',
                  borderRadius: 99,
                  padding: '0.1rem 0.5rem',
                  textTransform: 'uppercase',
                  letterSpacing: '0.05em',
                }}
              >
                {pendingProjects.length}
              </span>
            </div>
            <div style={{ fontSize: '0.8125rem', color: 'var(--text-muted)', marginTop: 2 }}>
              These projects were created via the setup page and are awaiting approval.
            </div>
          </div>
          {pendingProjects.map((p) => (
            <div
              key={p.id}
              style={{
                padding: '0.875rem 1.25rem',
                borderBottom: '1px solid var(--border)',
                display: 'flex',
                flexWrap: 'wrap',
                alignItems: 'center',
                gap: 12,
              }}
            >
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ fontSize: '0.875rem', fontWeight: 600 }}>{p.name}</span>
                  <span
                    style={{
                      fontSize: 11,
                      fontWeight: 700,
                      background: 'rgba(245,158,11,0.1)',
                      color: '#f59e0b',
                      border: '1px solid rgba(245,158,11,0.3)',
                      borderRadius: 99,
                      padding: '0.1rem 0.5rem',
                    }}
                  >
                    pending
                  </span>
                </div>
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: 2 }}>
                  {p.slug}
                </div>
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                <CopyButton value={`${window.location.origin}/api/v1/setup/${p.slug}`} />
                <a
                  href={`/api/v1/setup/${p.slug}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="btn btn-sm"
                  style={{ textDecoration: 'none', display: 'flex', alignItems: 'center', gap: 4 }}
                >
                  <ExternalLink size={13} />
                  Setup
                </a>
                <button
                  onClick={() => handleReject(p.id)}
                  disabled={rejectingProject === p.id}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 5,
                    background: 'rgba(239,68,68,0.1)',
                    border: '1px solid rgba(239,68,68,0.3)',
                    borderRadius: '0.5rem',
                    color: 'var(--error)',
                    padding: '0.375rem 0.75rem',
                    cursor: rejectingProject === p.id ? 'not-allowed' : 'pointer',
                    fontSize: '0.8125rem',
                    fontWeight: 700,
                    fontFamily: 'inherit',
                  }}
                >
                  <XCircle size={13} />
                  {rejectingProject === p.id ? 'Rejecting...' : 'Reject'}
                </button>
                <button
                  onClick={() => handleApprove(p.id)}
                  disabled={approvingProject === p.id}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 5,
                    background: approvingProject === p.id ? '#1a4a2e' : 'rgba(16,185,129,0.1)',
                    border: '1px solid rgba(16,185,129,0.3)',
                    borderRadius: '0.5rem',
                    color: 'var(--success)',
                    padding: '0.375rem 0.75rem',
                    cursor: approvingProject === p.id ? 'not-allowed' : 'pointer',
                    fontSize: '0.8125rem',
                    fontWeight: 700,
                    fontFamily: 'inherit',
                  }}
                >
                  <CheckCircle size={13} />
                  {approvingProject === p.id ? 'Approving...' : 'Approve'}
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Active projects with API keys */}
      <div className="card" style={{ padding: 0 }}>
        <div
          style={{
            padding: '1rem 1.25rem',
            borderBottom: '1px solid var(--border)',
          }}
        >
          <span style={{ fontWeight: 700, fontSize: '0.875rem' }}>Projects</span>
        </div>
        {activeProjects.length === 0 && projects.length === 0 ? (
          <div
            style={{
              padding: '2rem',
              textAlign: 'center',
              color: 'var(--text-muted)',
              fontSize: '0.875rem',
            }}
          >
            No projects yet. Use the setup URL above to create one.
          </div>
        ) : (
          activeProjects.map((p) => {
            const stats = statsMap[p.id]
            const errorRate = stats && stats.spanCount > 0
              ? ((stats.errorCount / stats.spanCount) * 100).toFixed(1)
              : null
            return (
              <div
                key={p.id}
                style={{
                  padding: '1rem 1.25rem',
                  borderBottom: '1px solid var(--border)',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
                  <span style={{ fontSize: '0.875rem', fontWeight: 600 }}>{p.name}</span>
                  <span
                    style={{
                      fontSize: 11,
                      fontWeight: 700,
                      background: 'rgba(34,197,94,0.15)',
                      color: '#22c55e',
                      borderRadius: 99,
                      padding: '0.1rem 0.5rem',
                    }}
                  >
                    {p.status}
                  </span>
                  <span className="mono text-muted" style={{ fontSize: '0.75rem' }}>
                    {p.slug}
                  </span>
                  {stats && (
                    <div style={{ display: 'flex', gap: 12, alignItems: 'center', fontSize: '0.75rem', marginLeft: 4 }}>
                      <span style={{ color: 'var(--text-muted)' }}>
                        <strong style={{ color: 'var(--text)', fontWeight: 700 }}>{formatNumber(stats.spanCount)}</strong> spans
                      </span>
                      {stats.errorCount > 0 && (
                        <span style={{ color: 'var(--text-muted)' }}>
                          <strong style={{ color: '#ef4444', fontWeight: 700 }}>{formatNumber(stats.errorCount)}</strong> errors
                          {errorRate && <span style={{ marginLeft: 3 }}>({errorRate}%)</span>}
                        </span>
                      )}
                    </div>
                  )}
                  <div style={{ marginLeft: 'auto', display: 'flex', gap: 6, alignItems: 'center' }}>
                    <CopyButton value={`${window.location.origin}/api/v1/setup/${p.slug}`} />
                    <a
                      href={`/api/v1/setup/${p.slug}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="btn btn-sm"
                      style={{ textDecoration: 'none', display: 'flex', alignItems: 'center', gap: 4 }}
                    >
                      <ExternalLink size={13} />
                      Setup
                    </a>
                  </div>
                </div>
                <ProjectAPIKeys projectId={p.id} />
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}

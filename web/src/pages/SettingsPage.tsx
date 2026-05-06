import { useState, useEffect, useCallback, type ReactElement } from 'react'
import { CheckCircle, ExternalLink, Copy, Check } from 'lucide-react'
import { fetchJSON } from '../api/client'

type Project = {
  id: number
  slug: string
  name: string
  status: string
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

export function SettingsPage(): ReactElement {
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [approvingProject, setApprovingProject] = useState<number | null>(null)
  const [error, setError] = useState('')

  const fetchProjects = useCallback(async () => {
    try {
      const data = await fetchJSON<Project[]>('/api/v1/projects')
      setProjects(data ?? [])
    } catch {
      setError('Failed to load projects')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
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
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                <span
                  className="mono"
                  style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}
                >
                  /api/v1/setup/{p.slug}
                </span>
                <CopyButton value={`${window.location.origin}/api/v1/setup/${p.slug}`} />
                <a
                  href={`/api/v1/setup/${p.slug}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="btn btn-sm"
                  style={{ textDecoration: 'none', display: 'flex', alignItems: 'center', gap: 4 }}
                >
                  <ExternalLink size={13} />
                  Open
                </a>
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

      {/* Active projects */}
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
            No projects yet. Use the setup page to create one:
            <code
              style={{
                display: 'block',
                marginTop: 8,
                color: 'var(--accent)',
                fontSize: '0.8125rem',
              }}
            >
              /api/v1/setup/your-project-slug
            </code>
          </div>
        ) : (
          <div style={{ overflowX: 'auto' }}>
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Slug</th>
                  <th>Status</th>
                  <th>Setup URL</th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                {activeProjects.map((p) => (
                  <tr key={p.id}>
                    <td style={{ fontWeight: 500 }}>{p.name}</td>
                    <td className="mono text-muted" style={{ fontSize: '0.8125rem' }}>
                      {p.slug}
                    </td>
                    <td>
                      <span
                        style={{
                          display: 'inline-block',
                          padding: '0.125rem 0.5rem',
                          borderRadius: '1rem',
                          fontSize: '0.75rem',
                          fontWeight: 600,
                          background: 'rgba(34,197,94,0.15)',
                          color: '#22c55e',
                        }}
                      >
                        {p.status}
                      </span>
                    </td>
                    <td>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        <CopyButton
                          value={`${window.location.origin}/api/v1/setup/${p.slug}`}
                        />
                        <a
                          href={`/api/v1/setup/${p.slug}`}
                          target="_blank"
                          rel="noopener noreferrer"
                          style={{ color: 'var(--accent)', fontSize: '0.8125rem' }}
                        >
                          Open
                        </a>
                      </div>
                    </td>
                    <td className="text-muted" style={{ fontSize: '0.8125rem' }}>
                      {new Date(p.createdAt).toLocaleDateString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}

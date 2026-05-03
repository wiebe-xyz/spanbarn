import type { ReactElement } from 'react'
import React, { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import type { TraceDetail } from '../api/types'
import { SpanWaterfall } from '../components/SpanWaterfall'
import { formatDuration } from '../utils/spanTree'

export function TraceDetailPage(): ReactElement {
  const { traceId } = useParams<{ traceId: string }>()
  const navigate = useNavigate()
  const [trace, setTrace] = useState<TraceDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  const apiBase = '/api/v1'

  useEffect(() => {
    if (!traceId) return
    setLoading(true)
    setError(null)
    fetch(`${apiBase}/traces/${traceId}`)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data: TraceDetail) => {
        setTrace(data)
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : 'Failed to load trace')
      })
      .finally(() => {
        setLoading(false)
      })
  }, [traceId])

  const copyTraceId = async () => {
    if (!traceId) return
    try {
      await navigator.clipboard.writeText(traceId)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      /* clipboard not available */
    }
  }

  const onBack = () => navigate('/traces')

  if (loading) {
    return (
      <div style={{ padding: 24, color: '#9ca3af' }}>Loading trace...</div>
    )
  }

  if (error) {
    return (
      <div style={{ padding: 24 }}>
        <button onClick={onBack} style={backButtonStyle}>
          Back to Traces
        </button>
        <div
          style={{
            marginTop: 16,
            padding: '8px 12px',
            background: 'rgba(239,68,68,0.1)',
            border: '1px solid #ef4444',
            borderRadius: 6,
            color: '#ef4444',
            fontSize: 13,
          }}
        >
          {error}
        </div>
      </div>
    )
  }

  if (!trace) return <div />

  const uniqueServices = new Set(trace.spans.map((s) => s.service))

  return (
    <div style={{ padding: 24 }}>
      {/* Back button */}
      <button onClick={onBack} style={backButtonStyle}>
        Back to Traces
      </button>

      {/* Header */}
      <div
        style={{
          marginTop: 16,
          marginBottom: 24,
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'start',
          flexWrap: 'wrap',
          gap: 16,
        }}
      >
        <div>
          <h1 style={{ fontSize: 20, fontWeight: 600, marginBottom: 4 }}>
            {trace.name || 'Trace Detail'}
          </h1>
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              fontSize: 13,
              color: '#9ca3af',
            }}
          >
            <code style={{ fontSize: 12 }}>{traceId}</code>
            <button
              onClick={copyTraceId}
              style={{
                background: 'transparent',
                border: '1px solid #374151',
                borderRadius: 4,
                padding: '2px 8px',
                color: '#9ca3af',
                fontSize: 11,
                cursor: 'pointer',
              }}
            >
              {copied ? 'Copied!' : 'Copy'}
            </button>
          </div>
        </div>

        {/* Summary stats */}
        <div style={{ display: 'flex', gap: 24, fontSize: 13 }}>
          <div style={{ textAlign: 'center' }}>
            <div style={{ color: '#9ca3af', fontSize: 11, marginBottom: 2 }}>
              Duration
            </div>
            <div style={{ fontWeight: 600, color: '#e5e7eb' }}>
              {formatDuration(trace.durationUs)}
            </div>
          </div>
          <div style={{ textAlign: 'center' }}>
            <div style={{ color: '#9ca3af', fontSize: 11, marginBottom: 2 }}>
              Services
            </div>
            <div style={{ fontWeight: 600, color: '#e5e7eb' }}>
              {uniqueServices.size}
            </div>
          </div>
          <div style={{ textAlign: 'center' }}>
            <div style={{ color: '#9ca3af', fontSize: 11, marginBottom: 2 }}>
              Spans
            </div>
            <div style={{ fontWeight: 600, color: '#e5e7eb' }}>
              {trace.spans.length}
            </div>
          </div>
        </div>
      </div>

      {/* Waterfall */}
      <SpanWaterfall spans={trace.spans} totalDurationUs={trace.durationUs} />
    </div>
  )
}

const backButtonStyle: React.CSSProperties = {
  background: 'transparent',
  border: '1px solid #374151',
  borderRadius: 6,
  padding: '6px 12px',
  color: '#9ca3af',
  fontSize: 13,
  cursor: 'pointer',
}

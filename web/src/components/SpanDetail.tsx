import { useState } from 'react'
import type { Span } from '../api/types'
import {
  formatDuration,
  kindColor,
  parseAttributes,
  parseEvents,
  statusColor,
} from '../utils/spanTree'
import './SpanTimeline.css'

type Props = {
  span: Span
  traceStartTimeUs: number
}

type Tab = 'overview' | 'attributes' | 'events'

export function SpanDetail({ span, traceStartTimeUs }: Props) {
  const [activeTab, setActiveTab] = useState<Tab>('overview')

  const attributes = parseAttributes(span.attributes)
  const events = parseEvents(span.events)
  const relativeStartUs = span.startTimeUs - traceStartTimeUs

  const tabs: { key: Tab; label: string }[] = [
    { key: 'overview', label: 'Overview' },
    { key: 'attributes', label: `Attributes (${Object.keys(attributes).length})` },
    { key: 'events', label: `Events (${events.length})` },
  ]

  return (
    <div className="span-detail" data-testid="span-detail">
      <div className="span-detail-header">
        <span className="span-detail-title">{span.name}</span>
        <span style={{ color: kindColor(span.kind), fontSize: 12 }}>
          {span.kind || 'internal'}
        </span>
      </div>

      <div className="span-detail-tabs">
        {tabs.map((tab) => (
          <button
            key={tab.key}
            className={`span-detail-tab ${activeTab === tab.key ? 'span-detail-tab--active' : ''}`}
            onClick={() => setActiveTab(tab.key)}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {activeTab === 'overview' && (
        <div className="span-detail-grid">
          <span className="span-detail-label">Service</span>
          <span className="span-detail-value">{span.service}</span>

          <span className="span-detail-label">Operation</span>
          <span className="span-detail-value">{span.name}</span>

          <span className="span-detail-label">Kind</span>
          <span className="span-detail-value">{span.kind || 'internal'}</span>

          <span className="span-detail-label">Status</span>
          <span
            className="span-detail-value"
            style={{ color: statusColor(span.status) }}
          >
            {span.status || 'ok'}
          </span>

          <span className="span-detail-label">Duration</span>
          <span className="span-detail-value">
            {formatDuration(span.durationUs)}
          </span>

          <span className="span-detail-label">Start (absolute)</span>
          <span className="span-detail-value">
            {new Date(span.startTimeUs / 1000).toISOString()}
          </span>

          <span className="span-detail-label">Start (relative)</span>
          <span className="span-detail-value">
            +{formatDuration(relativeStartUs)}
          </span>

          <span className="span-detail-label">Span ID</span>
          <span className="span-detail-value">{span.spanId}</span>

          {span.parentSpanId && (
            <>
              <span className="span-detail-label">Parent Span ID</span>
              <span className="span-detail-value">{span.parentSpanId}</span>
            </>
          )}

          {span.resource && (
            <>
              <span className="span-detail-label">Resource</span>
              <span className="span-detail-value">{span.resource}</span>
            </>
          )}
        </div>
      )}

      {activeTab === 'attributes' && (
        <table className="span-detail-table">
          <thead>
            <tr>
              <th>Key</th>
              <th>Value</th>
            </tr>
          </thead>
          <tbody>
            {Object.entries(attributes)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([key, value]) => (
                <tr key={key}>
                  <td>{key}</td>
                  <td>
                    {typeof value === 'object'
                      ? JSON.stringify(value)
                      : String(value)}
                  </td>
                </tr>
              ))}
            {Object.keys(attributes).length === 0 && (
              <tr>
                <td colSpan={2} style={{ color: '#6b7280', fontStyle: 'italic' }}>
                  No attributes
                </td>
              </tr>
            )}
          </tbody>
        </table>
      )}

      {activeTab === 'events' && (
        <div>
          {events.length === 0 && (
            <div style={{ color: '#6b7280', fontStyle: 'italic', fontSize: 12 }}>
              No events
            </div>
          )}
          {events.map((event, i) => {
            const offsetUs = event.timeUs - traceStartTimeUs
            return (
              <div className="event-item" key={i}>
                <span className="event-time">
                  +{formatDuration(offsetUs > 0 ? offsetUs : 0)}
                </span>
                <div>
                  <div className="event-name">{event.name}</div>
                  {event.attributes &&
                    Object.keys(event.attributes).length > 0 && (
                      <div className="event-attrs">
                        {Object.entries(event.attributes)
                          .map(
                            ([k, v]) =>
                              `${k}=${typeof v === 'object' ? JSON.stringify(v) : v}`,
                          )
                          .join(', ')}
                      </div>
                    )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

import React, { useState } from 'react'
import type { Span } from '../api/types'
import { SpanDetail } from './SpanDetail'
import {
  type FlatSpan,
  buildSpanTree,
  flattenTree,
  formatDuration,
  kindClass,
  kindColor,
} from '../utils/spanTree'
import './SpanTimeline.css'

type Props = {
  spans: Span[]
  totalDurationUs: number
}

export function SpanWaterfall({ spans, totalDurationUs }: Props) {
  const [selectedSpanId, setSelectedSpanId] = useState<string | null>(null)
  const [hoveredSpanId, setHoveredSpanId] = useState<string | null>(null)
  const [tooltipPos, setTooltipPos] = useState({ x: 0, y: 0 })

  const tree = buildSpanTree(spans)
  const flatSpans = flattenTree(tree)

  const traceStartTimeUs =
    spans.length > 0
      ? Math.min(...spans.map((s) => s.startTimeUs))
      : 0

  const traceEndTimeUs =
    spans.length > 0
      ? Math.max(...spans.map((s) => s.startTimeUs + s.durationUs))
      : 0

  const actualRange = traceEndTimeUs - traceStartTimeUs
  const safeDuration = actualRange > 0 ? actualRange : (totalDurationUs > 0 ? totalDurationUs : 1)

  const handleRowClick = (spanId: string) => {
    setSelectedSpanId(selectedSpanId === spanId ? null : spanId)
  }

  const navigateToSpan = (spanId: string) => {
    setSelectedSpanId(spanId)
    const row = document.querySelector(`[data-testid="waterfall-row-${spanId}"]`)
    if (row) {
      row.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }
  }

  const handleMouseMove = (e: React.MouseEvent, span: Span) => {
    setHoveredSpanId(span.spanId)
    setTooltipPos({ x: e.clientX + 12, y: e.clientY + 12 })
  }

  const handleMouseLeave = () => {
    setHoveredSpanId(null)
  }

  return (
    <div className="waterfall-container" data-testid="span-waterfall">
      {/* Header */}
      <div className="waterfall-header">
        <span className="waterfall-label" style={{ paddingLeft: 8 }}>Service / Operation</span>
        <span style={{ flex: 1 }}>Timeline</span>
        <span className="waterfall-duration" style={{ textAlign: 'right', paddingRight: 8 }}>
          Duration
        </span>
      </div>

      {/* Span rows */}
      {flatSpans.map((flat: FlatSpan) => {
        const { span, depth } = flat
        const leftPct =
          ((span.startTimeUs - traceStartTimeUs) / safeDuration) * 100
        const widthPct = (span.durationUs / safeDuration) * 100
        const isError = span.status?.toLowerCase() === 'error'
        const isSelected = selectedSpanId === span.spanId

        return (
          <React.Fragment key={span.spanId}>
            <div
              className={`waterfall-row ${isSelected ? 'waterfall-row--selected' : ''} ${isError ? 'waterfall-row--error' : ''}`}
              onClick={() => handleRowClick(span.spanId)}
              onMouseMove={(e) => handleMouseMove(e, span)}
              onMouseLeave={handleMouseLeave}
              data-testid={`waterfall-row-${span.spanId}`}
            >
              {/* Label with indentation */}
              <div
                className="waterfall-label"
                style={{ paddingLeft: 8 + depth * 16 }}
              >
                <span className="waterfall-label-service">
                  {span.service}
                </span>
                <span className="waterfall-label-name">{span.name}</span>
              </div>

              {/* Timeline bar */}
              <div className="waterfall-timeline" data-testid={`waterfall-timeline-${span.spanId}`}>
                <div
                  className={`waterfall-bar waterfall-bar--${kindClass(span.kind)} ${isError ? 'waterfall-bar--error' : ''}`}
                  data-testid={`waterfall-bar-${span.spanId}`}
                  style={{
                    left: `${Math.max(0, Math.min(leftPct, 100))}%`,
                    width: `${Math.max(0.5, Math.min(widthPct, 100 - leftPct))}%`,
                    minWidth: 2,
                    backgroundColor: kindColor(span.kind),
                  }}
                />
              </div>

              {/* Duration */}
              <div className="waterfall-duration">
                {formatDuration(span.durationUs)}
              </div>
            </div>

            {/* Detail panel */}
            {isSelected && (
              <SpanDetail
                span={span}
                traceStartTimeUs={traceStartTimeUs}
                onNavigateToSpan={navigateToSpan}
              />
            )}
          </React.Fragment>
        )
      })}

      {/* Tooltip */}
      {hoveredSpanId && !selectedSpanId && (
        <div
          className="waterfall-tooltip"
          style={{ left: tooltipPos.x, top: tooltipPos.y }}
        >
          {(() => {
            const span = spans.find((s) => s.spanId === hoveredSpanId)
            if (!span) return null
            return (
              <>
                <div className="waterfall-tooltip-row">
                  <span className="waterfall-tooltip-label">Service</span>
                  <span className="waterfall-tooltip-value">{span.service}</span>
                </div>
                <div className="waterfall-tooltip-row">
                  <span className="waterfall-tooltip-label">Operation</span>
                  <span className="waterfall-tooltip-value">{span.name}</span>
                </div>
                <div className="waterfall-tooltip-row">
                  <span className="waterfall-tooltip-label">Duration</span>
                  <span className="waterfall-tooltip-value">
                    {formatDuration(span.durationUs)}
                  </span>
                </div>
                <div className="waterfall-tooltip-row">
                  <span className="waterfall-tooltip-label">Kind</span>
                  <span className="waterfall-tooltip-value">
                    {span.kind || 'internal'}
                  </span>
                </div>
              </>
            )
          })()}
        </div>
      )}
    </div>
  )
}

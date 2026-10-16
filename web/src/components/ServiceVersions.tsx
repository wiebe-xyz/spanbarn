import { useEffect, useState, type ReactElement } from 'react'
import { api } from '../api/client'
import type { VersionSummary } from '../api/types'
import { getTimeRange } from '../utils/timeRange'
import { p95Change } from '../utils/versions'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'

/**
 * Versions of one service over the selected range, newest first. Renders
 * nothing while loading and for a service whose spans carry no
 * service.version.
 */
export function ServiceVersions({ service, range }: { service: string; range: string }): ReactElement | null {
  const [versions, setVersions] = useState<VersionSummary[]>([])

  useEffect(() => {
    let cancelled = false
    const { from, to } = getTimeRange(range)
    api
      .getServiceVersions(service, from, to)
      .then((v) => {
        if (!cancelled) setVersions(v ?? [])
      })
      .catch(() => {
        if (!cancelled) setVersions([])
      })
    return () => {
      cancelled = true
    }
  }, [service, range])

  if (versions.length === 0) return null

  return (
    <div className="card" style={{ padding: 0, overflow: 'hidden', marginTop: '1.5rem' }} data-testid="service-versions">
      <div style={{ fontSize: '0.875rem', fontWeight: 600, padding: '0.75rem 1rem' }}>Versions</div>
      <div style={{ overflowX: 'auto' }}>
        <table style={{ minWidth: 700 }}>
          <thead>
            <tr>
              <th>Version</th>
              <th style={{ textAlign: 'right' }}>Requests</th>
              <th style={{ textAlign: 'right' }}>Error Rate</th>
              <th style={{ textAlign: 'right' }}>P50</th>
              <th style={{ textAlign: 'right' }}>P95</th>
              <th style={{ textAlign: 'right' }}>P95 vs previous</th>
              <th style={{ textAlign: 'right' }}>P99</th>
            </tr>
          </thead>
          <tbody>
            {versions.map((v, i) => (
              <tr key={v.version}>
                <td className="mono">{v.version}</td>
                <td style={{ textAlign: 'right' }} className="mono">{formatCount(v.spanCount)}</td>
                <td style={{ textAlign: 'right', color: errorRateColor(v.errorRate) }} className="mono">
                  {formatErrorRate(v.errorRate)}
                </td>
                <td style={{ textAlign: 'right' }} className="mono">{formatDuration(v.p50Us)}</td>
                <td style={{ textAlign: 'right' }} className="mono">{formatDuration(v.p95Us)}</td>
                <td style={{ textAlign: 'right' }} className="mono">{p95Change(v, versions[i + 1]) || '-'}</td>
                <td style={{ textAlign: 'right' }} className="mono">{formatDuration(v.p99Us)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

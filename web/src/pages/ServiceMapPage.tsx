import { useState, useEffect, useCallback, useRef, type ReactElement } from 'react'
import { api } from '../api/client'
import type { ServiceMap, ServiceMapNode, ServiceMapEdge } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { AutoRefresh } from '../components/AutoRefresh'
import { getTimeRange } from '../utils/timeRange'
import { useTimeRange } from '../contexts/useTimeRange'
import { formatCount, formatErrorRate, errorRateColor } from '../utils/format'

type SimNode = ServiceMapNode & { x: number; y: number; vx: number; vy: number }

function useForceLayout(nodes: ServiceMapNode[], edges: ServiceMapEdge[], width: number, height: number) {
  const [simNodes, setSimNodes] = useState<SimNode[]>([])
  const frameRef = useRef(0)
  const iterRef = useRef(0)

  useEffect(() => {
    if (nodes.length === 0) {
      setSimNodes([]) // eslint-disable-line react-hooks/set-state-in-effect -- resetting derived state when input is empty
      return
    }

    const sn: SimNode[] = nodes.map((n, i) => {
      const angle = (2 * Math.PI * i) / nodes.length
      const r = Math.min(width, height) * 0.3
      return {
        ...n,
        x: width / 2 + r * Math.cos(angle),
        y: height / 2 + r * Math.sin(angle),
        vx: 0,
        vy: 0,
      }
    })

    const nodeIndex = new Map<string, number>()
    sn.forEach((n, i) => nodeIndex.set(n.id, i))

    iterRef.current = 0

    function tick() {
      const maxIter = 200
      if (iterRef.current >= maxIter) return

      const alpha = 1 - iterRef.current / maxIter
      const repulsion = 8000 * alpha
      const attraction = 0.005 * alpha
      const centerForce = 0.01 * alpha

      for (let i = 0; i < sn.length; i++) {
        for (let j = i + 1; j < sn.length; j++) {
          const dx = sn[j].x - sn[i].x
          const dy = sn[j].y - sn[i].y
          const dist = Math.max(Math.sqrt(dx * dx + dy * dy), 1)
          const force = repulsion / (dist * dist)
          const fx = (dx / dist) * force
          const fy = (dy / dist) * force
          sn[i].vx -= fx
          sn[i].vy -= fy
          sn[j].vx += fx
          sn[j].vy += fy
        }
      }

      for (const edge of edges) {
        const si = nodeIndex.get(edge.source)
        const ti = nodeIndex.get(edge.target)
        if (si === undefined || ti === undefined) continue
        const dx = sn[ti].x - sn[si].x
        const dy = sn[ti].y - sn[si].y
        const dist = Math.sqrt(dx * dx + dy * dy)
        const force = (dist - 200) * attraction
        const fx = (dx / Math.max(dist, 1)) * force
        const fy = (dy / Math.max(dist, 1)) * force
        sn[si].vx += fx
        sn[si].vy += fy
        sn[ti].vx -= fx
        sn[ti].vy -= fy
      }

      for (const n of sn) {
        n.vx += (width / 2 - n.x) * centerForce
        n.vy += (height / 2 - n.y) * centerForce
        n.vx *= 0.6
        n.vy *= 0.6
        n.x += n.vx
        n.y += n.vy
        n.x = Math.max(60, Math.min(width - 60, n.x))
        n.y = Math.max(40, Math.min(height - 40, n.y))
      }

      iterRef.current++
      setSimNodes([...sn])
      frameRef.current = requestAnimationFrame(tick)
    }

    frameRef.current = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(frameRef.current)
  }, [nodes, edges, width, height])

  return simNodes
}

function nodeRadius(node: ServiceMapNode): number {
  return Math.max(20, Math.min(40, 15 + Math.log2(node.spanCount + 1) * 3))
}

function nodeColor(node: ServiceMapNode): string {
  if (node.errorRate > 0.1) return '#ef4444'
  if (node.errorRate > 0.01) return '#f59e0b'
  return 'var(--accent)'
}

function edgeColor(edge: ServiceMapEdge): string {
  if (edge.errorRate > 0.1) return '#ef4444'
  if (edge.errorRate > 0.01) return '#f59e0b'
  return 'var(--border)'
}

export function ServiceMapPage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [data, setData] = useState<ServiceMap | null>(null)
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<string | null>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const [dims, setDims] = useState({ width: 900, height: 500 })

  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const obs = new ResizeObserver((entries) => {
      const { width, height } = entries[0].contentRect
      setDims({ width: Math.max(400, width), height: Math.max(300, height) })
    })
    obs.observe(el)
    return () => obs.disconnect()
  }, [])

  const fetchData = useCallback(async () => {
    const { from, to } = getTimeRange(range)
    try {
      const sm = await api.getServiceMap(from, to)
      setData(sm)
    } catch {
      // handled by client
    } finally {
      setLoading(false)
    }
  }, [range])

  useEffect(() => {
    void fetchData() // eslint-disable-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
  }, [fetchData])

  const nodes = data?.nodes ?? []
  const edges = data?.edges ?? []
  const simNodes = useForceLayout(nodes, edges, dims.width, dims.height)

  const nodeMap = new Map<string, SimNode>()
  simNodes.forEach((n) => nodeMap.set(n.id, n))

  const selectedNode = selected ? nodeMap.get(selected) : null
  const selectedEdges = selected
    ? edges.filter((e) => e.source === selected || e.target === selected)
    : []

  return (
    <div>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: '1.5rem',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>Service Map</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
          <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchData} />
          <TimeRangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div ref={containerRef} style={{ width: '100%', height: '60vh', minHeight: 300 }}>
          {loading ? (
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-muted)' }}>
              Loading service map...
            </div>
          ) : nodes.length === 0 ? (
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-muted)' }}>
              No service data found
            </div>
          ) : (
            <svg width={dims.width} height={dims.height} style={{ display: 'block' }}>
              <defs>
                <marker id="arrow" viewBox="0 0 10 6" refX="10" refY="3" markerWidth="8" markerHeight="6" orient="auto-start-reverse">
                  <path d="M 0 0 L 10 3 L 0 6 z" fill="var(--text-muted)" />
                </marker>
                <marker id="arrow-red" viewBox="0 0 10 6" refX="10" refY="3" markerWidth="8" markerHeight="6" orient="auto-start-reverse">
                  <path d="M 0 0 L 10 3 L 0 6 z" fill="#ef4444" />
                </marker>
              </defs>

              {edges.map((edge, i) => {
                const s = nodeMap.get(edge.source)
                const t = nodeMap.get(edge.target)
                if (!s || !t) return null
                const dx = t.x - s.x
                const dy = t.y - s.y
                const dist = Math.sqrt(dx * dx + dy * dy) || 1
                const sr = nodeRadius(s)
                const tr = nodeRadius(t)
                const x1 = s.x + (dx / dist) * sr
                const y1 = s.y + (dy / dist) * sr
                const x2 = t.x - (dx / dist) * (tr + 10)
                const y2 = t.y - (dy / dist) * (tr + 10)
                const color = edgeColor(edge)
                const isHighlighted = selected && (edge.source === selected || edge.target === selected)
                const opacity = selected ? (isHighlighted ? 1 : 0.15) : 0.6
                return (
                  <line
                    key={i}
                    x1={x1} y1={y1} x2={x2} y2={y2}
                    stroke={color}
                    strokeWidth={Math.max(1, Math.min(4, Math.log2(edge.callCount + 1)))}
                    opacity={opacity}
                    markerEnd={edge.errorRate > 0.1 ? 'url(#arrow-red)' : 'url(#arrow)'}
                  />
                )
              })}

              {simNodes.map((node) => {
                const r = nodeRadius(node)
                const color = nodeColor(node)
                const isSelected = selected === node.id
                const opacity = selected ? (isSelected || edges.some((e) => e.source === selected && e.target === node.id || e.target === selected && e.source === node.id) ? 1 : 0.25) : 1
                return (
                  <g
                    key={node.id}
                    style={{ cursor: 'pointer', opacity }}
                    onClick={() => setSelected(isSelected ? null : node.id)}
                  >
                    <circle cx={node.x} cy={node.y} r={r} fill={color} opacity={0.15} stroke={color} strokeWidth={isSelected ? 3 : 1.5} />
                    <text
                      x={node.x}
                      y={node.y + r + 14}
                      textAnchor="middle"
                      fill="var(--text)"
                      fontSize={11}
                      fontWeight={600}
                    >
                      {node.id}
                    </text>
                    <text
                      x={node.x}
                      y={node.y + 4}
                      textAnchor="middle"
                      fill={color}
                      fontSize={10}
                      fontWeight={700}
                    >
                      {formatCount(node.spanCount)}
                    </text>
                  </g>
                )
              })}
            </svg>
          )}
        </div>
      </div>

      {selectedNode && (
        <div className="card" style={{ marginTop: '1rem' }}>
          <h3 style={{ fontSize: '1rem', fontWeight: 700, marginBottom: '0.75rem' }}>{selectedNode.id}</h3>
          <div style={{ display: 'flex', gap: '2rem', flexWrap: 'wrap', marginBottom: '1rem' }}>
            <div>
              <span style={{ color: 'var(--text-muted)', fontSize: '0.75rem' }}>Spans</span>
              <div className="mono" style={{ fontWeight: 600 }}>{formatCount(selectedNode.spanCount)}</div>
            </div>
            <div>
              <span style={{ color: 'var(--text-muted)', fontSize: '0.75rem' }}>Errors</span>
              <div className="mono" style={{ fontWeight: 600 }}>{formatCount(selectedNode.errorCount)}</div>
            </div>
            <div>
              <span style={{ color: 'var(--text-muted)', fontSize: '0.75rem' }}>Error Rate</span>
              <div className="mono" style={{ fontWeight: 600, color: errorRateColor(selectedNode.errorRate) }}>
                {formatErrorRate(selectedNode.errorRate)}
              </div>
            </div>
          </div>

          {selectedEdges.length > 0 && (
            <table>
              <thead>
                <tr>
                  <th style={{ textAlign: 'left' }}>Direction</th>
                  <th style={{ textAlign: 'left' }}>Service</th>
                  <th style={{ textAlign: 'left' }}>Type</th>
                  <th style={{ textAlign: 'right' }}>Calls</th>
                  <th style={{ textAlign: 'right' }}>Error Rate</th>
                </tr>
              </thead>
              <tbody>
                {selectedEdges.map((e, i) => {
                  const isOutgoing = e.source === selected
                  return (
                    <tr key={i}>
                      <td>{isOutgoing ? '→ Out' : '← In'}</td>
                      <td style={{ fontWeight: 600, color: 'var(--accent)' }}>
                        {isOutgoing ? e.target : e.source}
                      </td>
                      <td>
                        <span style={{
                          display: 'inline-block',
                          padding: '0.125rem 0.5rem',
                          borderRadius: '1rem',
                          fontSize: '0.75rem',
                          fontWeight: 600,
                          background: 'rgba(59,130,246,0.15)',
                          color: '#3b82f6',
                        }}>
                          {e.targetType}
                        </span>
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">{formatCount(e.callCount)}</td>
                      <td style={{ textAlign: 'right', color: errorRateColor(e.errorRate) }} className="mono">
                        {formatErrorRate(e.errorRate)}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  )
}

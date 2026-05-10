import type { ReactElement } from 'react'

type Props = {
  data: number[]
  width?: number
  height?: number
  color?: string
  fillColor?: string
}

export function Sparkline({ data, width = 80, height = 20, color = 'var(--accent)', fillColor }: Props): ReactElement {
  if (data.length < 2) {
    return <span style={{ color: 'var(--text-muted)', fontSize: '0.6875rem' }}>-</span>
  }

  const min = Math.min(...data)
  const max = Math.max(...data)
  const range = max - min || 1
  const padding = 1

  const points = data.map((v, i) => {
    const x = (i / (data.length - 1)) * (width - padding * 2) + padding
    const y = height - padding - ((v - min) / range) * (height - padding * 2)
    return `${x},${y}`
  })

  const polyline = points.join(' ')

  const fillPoints = fillColor
    ? `${padding},${height - padding} ${polyline} ${width - padding},${height - padding}`
    : undefined

  return (
    <svg width={width} height={height} style={{ display: 'block' }}>
      {fillPoints && (
        <polygon points={fillPoints} fill={fillColor} opacity={0.15} />
      )}
      <polyline
        points={polyline}
        fill="none"
        stroke={color}
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { HeatmapChart } from './HeatmapChart'

const from = Date.UTC(2026, 9, 1, 12, 0)
const to = Date.UTC(2026, 9, 1, 13, 0)

describe('HeatmapChart', () => {
  it('draws one rect per populated cell with a tooltip', () => {
    const { container } = render(
      <HeatmapChart
        label="HEATMAP(duration)"
        fromMs={from}
        toMs={to}
        heatmap={{
          intervalSeconds: 600,
          cells: [
            { time: new Date(from).toISOString(), bucket: 10, lowerUs: 31, upperUs: 45, count: 4 },
            { time: new Date(from + 600_000).toISOString(), bucket: 12, lowerUs: 63, upperUs: 90, count: 9 },
          ],
        }}
      />,
    )
    const rects = container.querySelectorAll('rect')
    expect(rects).toHaveLength(2)
    expect(container.querySelector('title')?.textContent).toBe('4 spans, 31us to 45us')
  })

  it('draws the time axis but no cells for an empty window', () => {
    const { container } = render(
      <HeatmapChart label="HEATMAP(duration)" fromMs={from} toMs={to} heatmap={{ intervalSeconds: 600, cells: [] }} />,
    )
    expect(container.querySelectorAll('rect')).toHaveLength(0)
    expect(container.querySelectorAll('text').length).toBeGreaterThan(0)
  })

  it('exposes the chart to assistive tech', () => {
    const { getByRole } = render(
      <HeatmapChart label="HEATMAP(duration)" fromMs={from} toMs={to} heatmap={{ intervalSeconds: 600, cells: [] }} />,
    )
    expect(getByRole('img', { name: 'HEATMAP(duration)' })).toBeInTheDocument()
  })
})

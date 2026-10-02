import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
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

  describe('drag selection', () => {
    const heatmap = {
      intervalSeconds: 600,
      cells: [
        { time: new Date(from).toISOString(), bucket: 10, lowerUs: 31, upperUs: 45, count: 4 },
        { time: new Date(from + 600_000).toISOString(), bucket: 12, lowerUs: 63, upperUs: 90, count: 9 },
      ],
    }

    // The svg draws 1:1 in jsdom: a 600x200 box matches the 600x200 viewBox.
    function setup(onBrush: (s: unknown) => void) {
      const utils = render(<HeatmapChart label="HEATMAP(duration)" fromMs={from} toMs={to} heatmap={heatmap} onBrush={onBrush} />)
      const svg = utils.container.querySelector('svg')!
      svg.getBoundingClientRect = () => ({ left: 0, top: 0, width: 600, height: 200, right: 600, bottom: 200, x: 0, y: 0, toJSON: () => ({}) })
      return svg
    }

    // 56px of left padding, 536px of plot: a quarter of the hour is x=190.
    it('reports the dragged time range and the duration band it covers', () => {
      const onBrush = vi.fn()
      const svg = setup(onBrush)
      fireEvent.pointerDown(svg, { clientX: 190, clientY: 125 })
      fireEvent.pointerMove(svg, { clientX: 324, clientY: 130 })
      fireEvent.pointerUp(svg)

      expect(onBrush).toHaveBeenCalledTimes(1)
      const sel = onBrush.mock.calls[0][0]
      expect(sel.fromMs).toBeCloseTo(from + 900_000, -3)
      expect(sel.toMs).toBeCloseTo(from + 1_800_000, -3)
      // Bucket 10 spans [31us, 45us).
      expect(sel.minUs).toBe(31)
      expect(sel.maxUs).toBe(44)
    })

    it('drops the duration band when the drag covers the full height', () => {
      const onBrush = vi.fn()
      const svg = setup(onBrush)
      fireEvent.pointerDown(svg, { clientX: 190, clientY: 0 })
      fireEvent.pointerMove(svg, { clientX: 324, clientY: 200 })
      fireEvent.pointerUp(svg)

      expect(onBrush.mock.calls[0][0]).toMatchObject({ minUs: 0, maxUs: 0 })
    })

    it('draws the selection while dragging and removes it afterwards', () => {
      const svg = setup(vi.fn())
      fireEvent.pointerDown(svg, { clientX: 190, clientY: 125 })
      fireEvent.pointerMove(svg, { clientX: 324, clientY: 130 })
      expect(screen.getByTestId('heatmap-selection')).toBeInTheDocument()
      fireEvent.pointerUp(svg)
      expect(screen.queryByTestId('heatmap-selection')).toBeNull()
    })

    it('ignores a click that never moved', () => {
      const onBrush = vi.fn()
      const svg = setup(onBrush)
      fireEvent.pointerDown(svg, { clientX: 190, clientY: 125 })
      fireEvent.pointerUp(svg)
      expect(onBrush).not.toHaveBeenCalled()
    })
  })
})

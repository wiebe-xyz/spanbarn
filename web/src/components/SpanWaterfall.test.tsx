import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { SpanWaterfall } from './SpanWaterfall'
import type { Span } from '../api/types'

afterEach(() => {
  cleanup()
})

// Mock CSS import
vi.mock('./SpanTimeline.css', () => ({}))

function makeSpan(overrides: Partial<Span> = {}): Span {
  return {
    id: 1,
    projectId: 1,
    traceId: 'trace-1',
    spanId: 'span-1',
    parentSpanId: '',
    name: 'GET /api',
    service: 'api-server',
    resource: '',
    kind: 'server',
    status: 'ok',
    startTimeUs: 1_000_000,
    durationUs: 100_000,
    attributes: '{}',
    events: '[]',
    ingestedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('SpanWaterfall', () => {
  it('renders all span rows', () => {
    const spans: Span[] = [
      makeSpan({ spanId: 's1', name: 'root-op', startTimeUs: 1000 }),
      makeSpan({
        spanId: 's2',
        parentSpanId: 's1',
        name: 'child-op',
        startTimeUs: 2000,
      }),
      makeSpan({
        spanId: 's3',
        parentSpanId: 's1',
        name: 'sibling-op',
        startTimeUs: 3000,
      }),
    ]

    render(<SpanWaterfall spans={spans} totalDurationUs={200_000} />)

    expect(screen.getByTestId('waterfall-row-s1')).toBeDefined()
    expect(screen.getByTestId('waterfall-row-s2')).toBeDefined()
    expect(screen.getByTestId('waterfall-row-s3')).toBeDefined()
  })

  it('positions bars with correct left% and width%', () => {
    const spans: Span[] = [
      makeSpan({
        spanId: 's1',
        startTimeUs: 0,
        durationUs: 500,
      }),
    ]

    render(<SpanWaterfall spans={spans} totalDurationUs={1000} />)

    const bar = screen.getByTestId('waterfall-bar-s1')
    expect(bar.style.left).toBe('0%')
    expect(bar.style.width).toBe('50%')
  })

  it('opens detail panel on click', () => {
    const spans: Span[] = [
      makeSpan({ spanId: 's1', name: 'click-me' }),
    ]

    render(<SpanWaterfall spans={spans} totalDurationUs={100_000} />)

    // Detail panel should not exist initially
    expect(screen.queryByTestId('span-detail')).toBeNull()

    // Click the row
    fireEvent.click(screen.getByTestId('waterfall-row-s1'))

    // Detail panel should now be visible
    expect(screen.getByTestId('span-detail')).toBeDefined()
  })

  it('closes detail panel on second click', () => {
    const spans: Span[] = [
      makeSpan({ spanId: 's1', name: 'click-me' }),
    ]

    render(<SpanWaterfall spans={spans} totalDurationUs={100_000} />)

    const row = screen.getByTestId('waterfall-row-s1')

    // Open
    fireEvent.click(row)
    expect(screen.getByTestId('span-detail')).toBeDefined()

    // Close
    fireEvent.click(row)
    expect(screen.queryByTestId('span-detail')).toBeNull()
  })

  it('applies error class for error spans', () => {
    const spans: Span[] = [
      makeSpan({ spanId: 's1', status: 'error' }),
    ]

    render(<SpanWaterfall spans={spans} totalDurationUs={100_000} />)

    const row = screen.getByTestId('waterfall-row-s1')
    expect(row.className).toContain('waterfall-row--error')
  })

  it('applies correct kind class to bar', () => {
    const spans: Span[] = [
      makeSpan({ spanId: 's1', kind: 'client' }),
    ]

    render(<SpanWaterfall spans={spans} totalDurationUs={100_000} />)

    const bar = screen.getByTestId('waterfall-bar-s1')
    expect(bar.className).toContain('waterfall-bar--client')
  })
})

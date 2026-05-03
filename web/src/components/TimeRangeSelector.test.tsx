import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { TimeRangeSelector, getTimeRange } from './TimeRangeSelector'

describe('TimeRangeSelector', () => {
  it('renders all range options', () => {
    const onChange = vi.fn()
    render(<TimeRangeSelector value="1h" onChange={onChange} />)

    expect(screen.getByText('1h')).toBeDefined()
    expect(screen.getByText('4h')).toBeDefined()
    expect(screen.getByText('24h')).toBeDefined()
    expect(screen.getByText('7d')).toBeDefined()
    expect(screen.getByText('30d')).toBeDefined()
  })

  it('calls onChange with correct value when clicked', () => {
    const onChange = vi.fn()
    render(<TimeRangeSelector value="1h" onChange={onChange} />)

    fireEvent.click(screen.getByText('4h'))
    expect(onChange).toHaveBeenCalledWith('4h')

    fireEvent.click(screen.getByText('7d'))
    expect(onChange).toHaveBeenCalledWith('7d')
  })

  it('applies active style to selected range', () => {
    const onChange = vi.fn()
    render(<TimeRangeSelector value="24h" onChange={onChange} />)

    const active = screen.getByText('24h')
    expect(active.className).toContain('btn-active')

    const inactive = screen.getByText('1h')
    expect(inactive.className).not.toContain('btn-active')
  })
})

describe('getTimeRange', () => {
  it('returns from/to ISO strings for 1h range', () => {
    const range = getTimeRange('1h')
    const from = new Date(range.from)
    const to = new Date(range.to)
    const diffMs = to.getTime() - from.getTime()
    // Should be approximately 1 hour (within a few seconds tolerance)
    expect(diffMs).toBeGreaterThan(3_590_000)
    expect(diffMs).toBeLessThan(3_610_000)
    expect(range.label).toBe('1h')
  })

  it('defaults to 1h for unknown value', () => {
    const range = getTimeRange('unknown')
    const from = new Date(range.from)
    const to = new Date(range.to)
    const diffMs = to.getTime() - from.getTime()
    expect(diffMs).toBeGreaterThan(3_590_000)
    expect(diffMs).toBeLessThan(3_610_000)
  })
})

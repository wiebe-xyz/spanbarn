import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { TimeRangePicker } from './TimeRangePicker'

function setup(offset: number) {
  const onRangeChange = vi.fn()
  const onOffsetChange = vi.fn()
  render(<TimeRangePicker range="24h" offset={offset} onRangeChange={onRangeChange} onOffsetChange={onOffsetChange} />)
  return { onRangeChange, onOffsetChange }
}

describe('TimeRangePicker', () => {
  it('shows the current range label', () => {
    setup(0)
    expect(screen.getByRole('combobox', { name: 'Time range' })).toHaveValue('24h')
    expect(screen.getByRole('option', { name: 'Last 1 day' })).toBeInTheDocument()
  })

  it('reports a range change', () => {
    const { onRangeChange } = setup(0)
    fireEvent.change(screen.getByRole('combobox', { name: 'Time range' }), { target: { value: '4h' } })
    expect(onRangeChange).toHaveBeenCalledWith('4h')
  })

  it('steps back one window with the previous arrow', () => {
    const { onOffsetChange } = setup(1)
    fireEvent.click(screen.getByRole('button', { name: 'Previous window' }))
    expect(onOffsetChange).toHaveBeenCalledWith(2)
  })

  it('steps forward with the next arrow', () => {
    const { onOffsetChange } = setup(2)
    fireEvent.click(screen.getByRole('button', { name: 'Next window' }))
    expect(onOffsetChange).toHaveBeenCalledWith(1)
  })

  it('disables the next arrow at the live window', () => {
    const { onOffsetChange } = setup(0)
    const next = screen.getByRole('button', { name: 'Next window' })
    expect(next).toBeDisabled()
    fireEvent.click(next)
    expect(onOffsetChange).not.toHaveBeenCalled()
  })
})

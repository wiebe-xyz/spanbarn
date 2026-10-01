import { useState } from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { FilterBuilder } from './FilterBuilder'
import { emptyExpr, serializeFilter, type FilterExpr } from '../../filters/model'

vi.mock('../../api/client', () => ({
  api: {
    getAttributes: vi.fn((s: { key?: string }) =>
      Promise.resolve({
        scanned: 10,
        sample: 1,
        truncated: false,
        maxSpans: 100,
        keys: s.key
          ? [{ key: s.key, spans: 10, coverage: 1, distinct: 2, distinctCapped: false, top: [{ value: '/health', count: 7 }, { value: '/api', count: 3 }] }]
          : [
              { key: 'url.path', spans: 10, coverage: 1, distinct: 2, distinctCapped: false, top: [] },
              { key: 'http.response.status_code', spans: 10, coverage: 1, distinct: 2, distinctCapped: false, top: [] },
            ],
      }),
    ),
  },
}))

const state: { latest: FilterExpr } = { latest: emptyExpr() }

function Harness({ initial = emptyExpr() }: { initial?: FilterExpr }) {
  const [expr, setExpr] = useState(initial)
  const change = (next: FilterExpr) => {
    state.latest = next
    setExpr(next)
  }
  return <FilterBuilder value={expr} onChange={change} projectId={1} />
}

beforeEach(() => {
  state.latest = emptyExpr()
})

describe('FilterBuilder', () => {
  it('starts empty and adds, edits and removes rows', () => {
    render(<Harness />)
    expect(screen.queryByTestId('filter-row')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '+ Add filter' }))
    fireEvent.change(screen.getByLabelText('Filter key'), { target: { value: 'url.path' } })
    fireEvent.change(screen.getByLabelText('Filter operator'), { target: { value: 'starts-with' } })
    fireEvent.change(screen.getByLabelText('Filter value'), { target: { value: '/api' } })
    expect(JSON.parse(serializeFilter(state.latest))).toEqual({
      match: 'and',
      filters: [{ key: 'url.path', op: 'starts-with', value: '/api' }],
    })

    fireEvent.click(screen.getByLabelText('Remove filter'))
    expect(screen.queryByTestId('filter-row')).not.toBeInTheDocument()
    expect(serializeFilter(state.latest)).toBe('')
  })

  it('hides the value for exists and takes a list for in', () => {
    render(<Harness initial={{ match: 'and', filters: [{ key: 'user', op: '=', value: 'ann' }] }} />)
    fireEvent.change(screen.getByLabelText('Filter operator'), { target: { value: 'exists' } })
    expect(screen.queryByLabelText('Filter value')).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Filter operator'), { target: { value: 'in' } })
    fireEvent.change(screen.getByLabelText('Filter value'), { target: { value: 'ann, bob' } })
    expect(JSON.parse(serializeFilter(state.latest)).filters[0]).toEqual({ key: 'user', op: 'in', values: ['ann', 'bob'] })
  })

  it('chooses between AND and OR once there are two rows', () => {
    render(<Harness />)
    expect(screen.queryByLabelText('Match')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '+ Add filter' }))
    fireEvent.click(screen.getByRole('button', { name: '+ Add filter' }))
    fireEvent.change(screen.getByLabelText('Match'), { target: { value: 'or' } })
    expect(state.latest.match).toBe('or')
  })

  it('adds one level of group with its own match', () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: '+ Add group' }))
    const group = screen.getByRole('group', { name: 'Filter group' })
    expect(group).toBeInTheDocument()
    expect(screen.getAllByLabelText('Filter key')).toHaveLength(2)
    fireEvent.change(screen.getByLabelText('Group match'), { target: { value: 'and' } })
    const node = state.latest.filters[0]
    expect(node).toMatchObject({ match: 'and' })
    // A group has no "add group" of its own: the model allows one level.
    expect(screen.getAllByRole('button', { name: '+ Add group' })).toHaveLength(1)

    fireEvent.click(screen.getByRole('button', { name: 'Remove group' }))
    expect(state.latest.filters).toHaveLength(0)
  })

  it('suggests attribute keys from discovery', async () => {
    const { container } = render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: '+ Add filter' }))
    await waitFor(() => {
      const options = Array.from(container.querySelectorAll('datalist option')).map((o) => o.getAttribute('value'))
      expect(options).toContain('url.path')
      expect(options).toContain('service')
    })
  })

  it('suggests the observed values of the chosen key when the value box gets focus', async () => {
    const { container } = render(<Harness initial={{ match: 'and', filters: [{ key: 'url.path', op: '=', value: '' }] }} />)
    fireEvent.focus(screen.getByLabelText('Filter value'))
    await waitFor(() => {
      const values = Array.from(container.querySelectorAll('datalist option')).map((o) => o.getAttribute('value'))
      expect(values).toContain('/health')
      expect(values).toContain('/api')
    })
  })
})

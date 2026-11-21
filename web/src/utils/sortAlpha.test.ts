import { describe, it, expect } from 'vitest'
import { sortAlpha } from './sortAlpha'

describe('sortAlpha', () => {
  it('orders strings ignoring case', () => {
    expect(sortAlpha(['qr', 'Profotograaf', 'funnelbarn', 'node', 'maatwerksoftware-web'])).toEqual([
      'funnelbarn', 'maatwerksoftware-web', 'node', 'Profotograaf', 'qr',
    ])
  })

  it('orders objects by the label it is given', () => {
    const projects = [{ id: 2, name: 'beta' }, { id: 1, name: 'Alpha' }, { id: 3, name: 'gamma' }]
    expect(sortAlpha(projects, (p) => p.name).map((p) => p.id)).toEqual([1, 2, 3])
  })

  it('reads digits as numbers', () => {
    expect(sortAlpha(['node-10', 'node-2', 'node-1'])).toEqual(['node-1', 'node-2', 'node-10'])
  })

  it('leaves the input untouched', () => {
    const input = ['b', 'a']
    sortAlpha(input)
    expect(input).toEqual(['b', 'a'])
  })
})

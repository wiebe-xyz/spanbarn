import { describe, it, expect } from 'vitest'
import {
  andWith,
  describeFilter,
  emptyExpr,
  hasFilters,
  parseFilter,
  pruneExpr,
  serializeFilter,
  type FilterExpr,
} from './model'

const sample: FilterExpr = {
  match: 'and',
  filters: [
    { key: 'kind', op: '=', value: 'server' },
    {
      match: 'or',
      filters: [
        { key: 'url.path', op: 'starts-with', value: '/api/v1/library' },
        { key: 'http.response.status_code', op: '>=', value: '500' },
      ],
    },
    { key: 'user', op: 'in', values: ['ann', 'bob'] },
    { key: 'cache', op: 'exists' },
  ],
}

describe('filter model', () => {
  it('serializes nothing for an empty or unfinished filter', () => {
    expect(serializeFilter(emptyExpr())).toBe('')
    expect(
      serializeFilter({ match: 'and', filters: [{ key: '', op: '=', value: 'x' }, { key: 'a', op: 'in', values: [''] }] }),
    ).toBe('')
    expect(hasFilters(emptyExpr())).toBe(false)
  })

  it('round trips through the URL form', () => {
    const raw = serializeFilter(sample)
    expect(parseFilter(raw)).toEqual(sample)
    expect(serializeFilter(parseFilter(raw))).toBe(raw)
  })

  it('matches the JSON the server reads', () => {
    expect(JSON.parse(serializeFilter(sample))).toEqual({
      match: 'and',
      filters: [
        { key: 'kind', op: '=', value: 'server' },
        {
          match: 'or',
          filters: [
            { key: 'url.path', op: 'starts-with', value: '/api/v1/library' },
            { key: 'http.response.status_code', op: '>=', value: '500' },
          ],
        },
        { key: 'user', op: 'in', values: ['ann', 'bob'] },
        { key: 'cache', op: 'exists' },
      ],
    })
  })

  it('reads number values from a saved query', () => {
    const e = parseFilter({ filters: [{ key: 'a', op: '=', value: 500 }] })
    expect(e.filters).toEqual([{ key: 'a', op: '=', value: '500' }])
  })

  it('treats bad input as no filter', () => {
    for (const bad of ['{', 'null', '[]', '{"filters":5}', null, undefined, 3]) {
      expect(parseFilter(bad)).toEqual(emptyExpr())
    }
    expect(parseFilter('{"filters":[{"key":"a","op":"~"}]}').filters).toEqual([])
  })

  it('prunes empty groups', () => {
    const e = pruneExpr({ match: 'and', filters: [{ match: 'or', filters: [{ key: '', op: '=' }] }] })
    expect(e.filters).toEqual([])
  })

  it('ANDs extra conditions onto an AND root', () => {
    const e = andWith([{ key: 'service', op: '=', value: 'web' }], {
      match: 'and',
      filters: [{ key: 'a', op: 'exists' }],
    })
    expect(e.filters).toHaveLength(2)
    expect(e.match).toBe('and')
  })

  it('distributes extra conditions over an OR root', () => {
    const e = andWith([{ key: 'service', op: '=', value: 'web' }], {
      match: 'or',
      filters: [
        { key: 'a', op: 'exists' },
        { match: 'or', filters: [{ key: 'b', op: 'exists' }, { key: 'c', op: 'exists' }] },
      ],
    })
    expect(e.match).toBe('or')
    expect(e.filters).toHaveLength(3)
    for (const g of e.filters) {
      expect(g).toHaveProperty('filters')
      expect((g as { filters: unknown[] }).filters).toHaveLength(2)
    }
  })

  it('uses only the extras when the expression is empty', () => {
    expect(andWith([{ key: 'service', op: '=', value: 'web' }], emptyExpr()).filters).toHaveLength(1)
  })

  it('describes a filter in one line', () => {
    expect(describeFilter(sample)).toBe(
      'kind = server and (url.path starts-with /api/v1/library or http.response.status_code >= 500) and user in ann,bob and cache exists',
    )
  })
})

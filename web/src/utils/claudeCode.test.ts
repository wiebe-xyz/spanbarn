import { describe, it, expect } from 'vitest'
import { joinTotals, toolCounts, formatCount, formatSeconds } from './claudeCode'
import type { LogEntry } from '../api/types'

const ev = (body: string, attributes: Record<string, unknown> = {}): LogEntry => ({
  id: 1, traceId: '', spanId: '', severityNumber: 9, severityText: '', timeUnixNano: 0, body, attributes, ingestedAt: '',
})

describe('joinTotals', () => {
  it('joins metrics by group value and sorts by the ordering metric', () => {
    const rows = joinTotals(
      'repo',
      {
        cost: [{ labels: { repo: 'a' }, total: 1 }, { labels: { repo: 'b' }, total: 5 }],
        tokens: [{ labels: { repo: 'a' }, total: 900 }],
      },
      'cost',
    )
    expect(rows.map((r) => r.value)).toEqual(['b', 'a'])
    expect(rows[1].totals).toEqual({ cost: 1, tokens: 900 })
  })

  it('skips groups without the key', () => {
    expect(joinTotals('repo', { cost: [{ labels: {}, total: 3 }] }, 'cost')).toEqual([])
  })
})

describe('toolCounts', () => {
  it('counts tool_result events per tool, largest first', () => {
    const counts = toolCounts([
      ev('claude_code.tool_result', { tool_name: 'Bash' }),
      ev('claude_code.tool_result', { tool_name: 'Read' }),
      ev('claude_code.tool_result', { tool_name: 'Bash' }),
      ev('claude_code.user_prompt'),
    ])
    expect(counts).toEqual([['Bash', 2], ['Read', 1]])
  })
})

describe('formatting', () => {
  it('abbreviates counts and durations', () => {
    expect(formatCount(1234)).toBe('1.2k')
    expect(formatCount(2_500_000)).toBe('2.5M')
    expect(formatSeconds(90)).toBe('2m')
    expect(formatSeconds(7200)).toBe('2.0h')
  })
})

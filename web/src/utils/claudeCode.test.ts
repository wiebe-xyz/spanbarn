import { describe, it, expect } from 'vitest'
import { joinTotals, toolCounts, toolSpanStats, formatCount, formatMicros, formatSeconds } from './claudeCode'
import type { LogEntry, OperationSummary } from '../api/types'

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

describe('toolSpanStats', () => {
  const op = (operation: string, resource: string, spanCount: number, p50Us = 0, p95Us = 0): OperationSummary => ({
    operation, resource, kind: 'internal', spanCount, errorCount: 0, errorRate: 0, p50Us, p95Us, p99Us: p95Us,
  })

  it('counts tool spans per tool and reports blocked-on-user time per tool', () => {
    const stats = toolSpanStats([
      op('claude_code.tool', 'Bash', 4),
      op('claude_code.tool', 'Read', 9),
      op('claude_code.tool.blocked_on_user', 'Bash', 2, 3_000_000, 8_000_000),
      op('claude_code.tool.execution', 'Bash', 4),
      op('claude_code.interaction', '', 3),
    ])
    expect(stats.tools).toEqual([
      { name: 'Read', calls: 9, blocked: 0, blockedP50Us: 0, blockedP95Us: 0 },
      { name: 'Bash', calls: 4, blocked: 2, blockedP50Us: 3_000_000, blockedP95Us: 8_000_000 },
    ])
    expect(stats.totalCalls).toBe(13)
    expect(stats.totalBlocked).toBe(2)
  })

  it('names tools without a resource unknown and returns nothing for no tool spans', () => {
    expect(toolSpanStats([op('claude_code.tool', '', 1)]).tools[0].name).toBe('unknown')
    expect(toolSpanStats([op('claude_code.interaction', '', 3)])).toEqual({ tools: [], totalCalls: 0, totalBlocked: 0 })
  })
})

describe('formatMicros', () => {
  it('formats microseconds as ms, seconds or minutes', () => {
    expect(formatMicros(450)).toBe('0 ms')
    expect(formatMicros(250_000)).toBe('250 ms')
    expect(formatMicros(3_400_000)).toBe('3.4 s')
    expect(formatMicros(90_000_000)).toBe('1.5 min')
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

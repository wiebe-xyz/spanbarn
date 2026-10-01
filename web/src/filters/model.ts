/**
 * The shared span filter model. It mirrors internal/filter: one JSON form for
 * the API `filter` parameter, saved queries and the `filter` parameter of the
 * page URL.
 */

export type FilterOp =
  | '='
  | '!='
  | '>'
  | '<'
  | '>='
  | '<='
  | 'contains'
  | 'starts-with'
  | 'exists'
  | 'does-not-exist'
  | 'in'
  | 'not-in'

export type Match = 'and' | 'or'

export type Condition = {
  key: string
  op: FilterOp
  value?: string
  values?: string[]
}

/** A nested group. The model allows one level of nesting. */
export type Group = { match: Match; filters: Condition[] }

export type FilterNode = Condition | Group

export type FilterExpr = { match: Match; filters: FilterNode[] }

export const OPERATORS: { op: FilterOp; label: string }[] = [
  { op: '=', label: 'is' },
  { op: '!=', label: 'is not' },
  { op: '>', label: '>' },
  { op: '<', label: '<' },
  { op: '>=', label: '>=' },
  { op: '<=', label: '<=' },
  { op: 'contains', label: 'contains' },
  { op: 'starts-with', label: 'starts with' },
  { op: 'exists', label: 'exists' },
  { op: 'does-not-exist', label: 'does not exist' },
  { op: 'in', label: 'is one of' },
  { op: 'not-in', label: 'is none of' },
]

const KNOWN_OPS = new Set<string>(OPERATORS.map((o) => o.op))

export const emptyExpr = (): FilterExpr => ({ match: 'and', filters: [] })

export const emptyCondition = (): Condition => ({ key: '', op: '=', value: '' })

export function isGroup(n: FilterNode): n is Group {
  return 'filters' in n
}

export function takesNoValue(op: FilterOp): boolean {
  return op === 'exists' || op === 'does-not-exist'
}

export function takesList(op: FilterOp): boolean {
  return op === 'in' || op === 'not-in'
}

/** True when the row is filled in enough to send. */
export function isComplete(c: Condition): boolean {
  if (!c.key.trim()) return false
  if (takesNoValue(c.op)) return true
  if (takesList(c.op)) return (c.values ?? []).some((v) => v !== '')
  return true
}

function cleanCondition(c: Condition): Condition | null {
  if (!isComplete(c)) return null
  const key = c.key.trim()
  if (takesNoValue(c.op)) return { key, op: c.op }
  if (takesList(c.op)) return { key, op: c.op, values: (c.values ?? []).filter((v) => v !== '') }
  return { key, op: c.op, value: c.value ?? '' }
}

/** Drops rows that are not filled in and groups left empty. */
export function pruneExpr(expr: FilterExpr): FilterExpr {
  const filters: FilterNode[] = []
  for (const n of expr.filters) {
    if (isGroup(n)) {
      const inner = n.filters.map(cleanCondition).filter((c): c is Condition => c !== null)
      if (inner.length > 0) filters.push({ match: n.match, filters: inner })
      continue
    }
    const c = cleanCondition(n)
    if (c) filters.push(c)
  }
  return { match: expr.match, filters }
}

export function hasFilters(expr: FilterExpr): boolean {
  return pruneExpr(expr).filters.length > 0
}

/** The JSON sent to the API and kept in the URL. Empty when nothing is set. */
export function serializeFilter(expr: FilterExpr): string {
  const pruned = pruneExpr(expr)
  return pruned.filters.length > 0 ? JSON.stringify(pruned) : ''
}

function parseCondition(raw: unknown): Condition | null {
  if (typeof raw !== 'object' || raw === null) return null
  const r = raw as Record<string, unknown>
  if (typeof r.key !== 'string' || typeof r.op !== 'string' || !KNOWN_OPS.has(r.op)) return null
  const c: Condition = { key: r.key, op: r.op as FilterOp }
  if (r.value !== undefined && r.value !== null) c.value = String(r.value)
  if (Array.isArray(r.values)) c.values = r.values.map(String)
  return c
}

function parseMatch(v: unknown): Match {
  return v === 'or' ? 'or' : 'and'
}

function parseNode(raw: unknown): FilterNode | null {
  if (typeof raw !== 'object' || raw === null) return null
  const r = raw as Record<string, unknown>
  if (!Array.isArray(r.filters)) return parseCondition(raw)
  const inner = r.filters.map(parseCondition).filter((c): c is Condition => c !== null)
  return inner.length > 0 ? { match: parseMatch(r.match), filters: inner } : null
}

/** Parses the JSON form from a URL or a saved query. Bad input gives an empty filter. */
export function parseFilter(raw: unknown): FilterExpr {
  let doc: unknown = raw
  if (typeof raw === 'string') {
    if (!raw.trim()) return emptyExpr()
    try {
      doc = JSON.parse(raw)
    } catch {
      return emptyExpr()
    }
  }
  if (typeof doc !== 'object' || doc === null) return emptyExpr()
  const r = doc as Record<string, unknown>
  if (!Array.isArray(r.filters)) return emptyExpr()
  return {
    match: parseMatch(r.match),
    filters: r.filters.map(parseNode).filter((n): n is FilterNode => n !== null),
  }
}

/**
 * ANDs conditions onto an expression and keeps it inside the one-level model.
 * An OR root is distributed: (a or b) and e becomes (a and e) or (b and e), with
 * OR groups of the root flattened into it first.
 */
export function andWith(extra: Condition[], expr: FilterExpr): FilterExpr {
  const pruned = pruneExpr(expr)
  if (extra.length === 0 || pruned.filters.length === 0) {
    return extra.length === 0 ? pruned : { match: 'and', filters: extra }
  }
  if (pruned.match === 'or' && pruned.filters.length > 1) {
    const terms = pruned.filters.flatMap((n): FilterNode[] => (isGroup(n) && n.match === 'or' ? n.filters : [n]))
    return {
      match: 'or',
      filters: terms.map((n): Group => ({
        match: 'and',
        filters: [...extra, ...(isGroup(n) ? n.filters : [n])],
      })),
    }
  }
  return { match: 'and', filters: [...extra, ...pruned.filters] }
}

function describeCondition(c: Condition): string {
  if (takesNoValue(c.op)) return `${c.key} ${c.op}`
  if (takesList(c.op)) return `${c.key} ${c.op} ${(c.values ?? []).join(',')}`
  return `${c.key} ${c.op} ${c.value ?? ''}`
}

/** A short text form, used to name a saved query. */
export function describeFilter(expr: FilterExpr): string {
  const joiner = expr.match === 'or' ? ' or ' : ' and '
  return pruneExpr(expr)
    .filters.map((n) => (isGroup(n) ? `(${n.filters.map(describeCondition).join(n.match === 'or' ? ' or ' : ' and ')})` : describeCondition(n)))
    .join(joiner)
}

/** The fixed filter fields of the traces page as conditions, for saving them with an expression. */
export function fieldConditions(f: { service: string; operation: string; status: string; minDurationMs: string }): Condition[] {
  const out: Condition[] = []
  if (f.service) out.push({ key: 'service', op: '=', value: f.service })
  if (f.operation) out.push({ key: 'name', op: '=', value: f.operation })
  if (f.status && f.status !== 'all') out.push({ key: 'status', op: '=', value: f.status })
  const us = Math.round(parseFloat(f.minDurationMs) * 1000)
  if (us > 0) out.push({ key: 'duration_us', op: '>=', value: String(us) })
  return out
}

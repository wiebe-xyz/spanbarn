/**
 * Span tree utilities for waterfall visualization.
 */

/** Raw span as returned by the SpanBarn API (/api/v1/traces/:traceId). */
export type Span = {
  id: number;
  projectId: number;
  traceId: string;
  spanId: string;
  parentSpanId: string;
  name: string;
  service: string;
  resource: string;
  kind: string;
  status: string;
  startTimeUs: number;
  durationUs: number;
  attributes: string;
  events: string;
  ingestedAt: string;
};

/** Node in the span tree with children and depth tracking. */
export type SpanNode = {
  span: Span;
  children: SpanNode[];
  depth: number;
};

/** Flattened span for rendering in the waterfall list. */
export type FlatSpan = {
  span: Span;
  depth: number;
};

/** Trace summary as returned by /api/v1/traces search endpoint. */
export type TraceSummary = {
  traceId: string;
  rootSpanName: string;
  rootService: string;
  durationUs: number;
  spanCount: number;
  status: string;
  startTime: string;
};

/** Trace detail as returned by /api/v1/traces/:traceId. */
export type TraceDetail = {
  traceId: string;
  spans: Span[];
  durationUs: number;
  service: string;
  name: string;
};

/** Parsed span event. */
export type SpanEvent = {
  name: string;
  timeUs: number;
  attributes?: Record<string, unknown>;
};

/**
 * Build a parent-child tree from a flat list of spans.
 * Spans whose parentSpanId doesn't match any span in the list become roots.
 */
export function buildSpanTree(spans: Span[]): SpanNode[] {
  const nodeMap = new Map<string, SpanNode>();
  const roots: SpanNode[] = [];

  // Create nodes
  for (const span of spans) {
    nodeMap.set(span.spanId, { span, children: [], depth: 0 });
  }

  // Link parents
  for (const span of spans) {
    const node = nodeMap.get(span.spanId)!;
    if (span.parentSpanId && nodeMap.has(span.parentSpanId)) {
      nodeMap.get(span.parentSpanId)!.children.push(node);
    } else {
      roots.push(node);
    }
  }

  // Sort children by start time
  const sortChildren = (node: SpanNode) => {
    node.children.sort((a, b) => a.span.startTimeUs - b.span.startTimeUs);
    node.children.forEach(sortChildren);
  };
  roots.sort((a, b) => a.span.startTimeUs - b.span.startTimeUs);
  roots.forEach(sortChildren);

  return roots;
}

/**
 * Flatten a span tree into a list with depth information for rendering.
 */
export function flattenTree(nodes: SpanNode[], depth: number = 0): FlatSpan[] {
  const result: FlatSpan[] = [];
  for (const node of nodes) {
    result.push({ span: node.span, depth });
    result.push(...flattenTree(node.children, depth + 1));
  }
  return result;
}

/** Format microseconds to a human-readable duration string. */
export function formatDuration(us: number): string {
  if (us < 1000) return `${us}µs`;
  if (us < 1_000_000) return `${(us / 1000).toFixed(1)}ms`;
  return `${(us / 1_000_000).toFixed(2)}s`;
}

/** Get CSS class suffix for span kind. */
export function kindClass(kind: string): string {
  switch (kind.toLowerCase()) {
    case "server":
      return "server";
    case "client":
      return "client";
    case "producer":
      return "producer";
    case "consumer":
      return "consumer";
    default:
      return "internal";
  }
}

/** Get color for span kind (used inline). */
export function kindColor(kind: string): string {
  switch (kind.toLowerCase()) {
    case "server":
      return "#3b82f6";
    case "client":
      return "#22c55e";
    case "producer":
      return "#a855f7";
    case "consumer":
      return "#f97316";
    default:
      return "#6b7280";
  }
}

/** Parse JSON attributes string into a record. */
export function parseAttributes(raw: string): Record<string, unknown> {
  if (!raw || raw === "null") return {};
  try {
    return JSON.parse(raw);
  } catch {
    return {};
  }
}

/** Parse JSON events string into an array. */
export function parseEvents(raw: string): SpanEvent[] {
  if (!raw || raw === "null") return [];
  try {
    return JSON.parse(raw);
  } catch {
    return [];
  }
}

/** Duration color: green < 100ms, yellow < 1s, red >= 1s. */
export function durationColor(us: number): string {
  if (us < 100_000) return "#22c55e";
  if (us < 1_000_000) return "#eab308";
  return "#ef4444";
}

/** Status color: green for ok, red for error. */
export function statusColor(status: string): string {
  return status.toLowerCase() === "error" ? "#ef4444" : "#22c55e";
}

/** Truncate a trace/span ID for display. */
export function truncateId(id: string, len: number = 8): string {
  return id.length > len ? id.slice(0, len) + "…" : id;
}

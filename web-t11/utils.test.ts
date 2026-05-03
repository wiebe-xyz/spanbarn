import { describe, it, expect } from "vitest";
import {
  type Span,
  buildSpanTree,
  flattenTree,
  formatDuration,
  durationColor,
  statusColor,
  truncateId,
  kindClass,
  parseAttributes,
  parseEvents,
} from "./utils";

/** Helper to create a minimal Span for testing. */
function makeSpan(overrides: Partial<Span> = {}): Span {
  return {
    id: 1,
    projectId: 1,
    traceId: "trace-1",
    spanId: "span-1",
    parentSpanId: "",
    name: "GET /api",
    service: "api-server",
    resource: "",
    kind: "server",
    status: "ok",
    startTimeUs: 1000000,
    durationUs: 50000,
    attributes: "{}",
    events: "[]",
    ingestedAt: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("buildSpanTree", () => {
  it("builds a 3-level tree (root -> child -> grandchild)", () => {
    const spans: Span[] = [
      makeSpan({ spanId: "root", parentSpanId: "", startTimeUs: 1000 }),
      makeSpan({ spanId: "child", parentSpanId: "root", startTimeUs: 2000 }),
      makeSpan({
        spanId: "grandchild",
        parentSpanId: "child",
        startTimeUs: 3000,
      }),
    ];

    const tree = buildSpanTree(spans);

    expect(tree).toHaveLength(1);
    expect(tree[0].span.spanId).toBe("root");
    expect(tree[0].children).toHaveLength(1);
    expect(tree[0].children[0].span.spanId).toBe("child");
    expect(tree[0].children[0].children).toHaveLength(1);
    expect(tree[0].children[0].children[0].span.spanId).toBe("grandchild");
    expect(tree[0].children[0].children[0].children).toHaveLength(0);
  });

  it("handles multiple roots", () => {
    const spans: Span[] = [
      makeSpan({ spanId: "root-a", parentSpanId: "", startTimeUs: 1000 }),
      makeSpan({ spanId: "root-b", parentSpanId: "", startTimeUs: 2000 }),
    ];

    const tree = buildSpanTree(spans);

    expect(tree).toHaveLength(2);
    expect(tree[0].span.spanId).toBe("root-a");
    expect(tree[1].span.spanId).toBe("root-b");
  });

  it("treats orphaned spans (missing parent) as roots", () => {
    const spans: Span[] = [
      makeSpan({ spanId: "root", parentSpanId: "", startTimeUs: 1000 }),
      makeSpan({
        spanId: "orphan",
        parentSpanId: "nonexistent",
        startTimeUs: 2000,
      }),
    ];

    const tree = buildSpanTree(spans);

    expect(tree).toHaveLength(2);
    const ids = tree.map((n) => n.span.spanId).sort();
    expect(ids).toEqual(["orphan", "root"]);
  });

  it("sorts children by start time", () => {
    const spans: Span[] = [
      makeSpan({ spanId: "root", parentSpanId: "", startTimeUs: 1000 }),
      makeSpan({
        spanId: "child-late",
        parentSpanId: "root",
        startTimeUs: 5000,
      }),
      makeSpan({
        spanId: "child-early",
        parentSpanId: "root",
        startTimeUs: 2000,
      }),
    ];

    const tree = buildSpanTree(spans);

    expect(tree[0].children[0].span.spanId).toBe("child-early");
    expect(tree[0].children[1].span.spanId).toBe("child-late");
  });
});

describe("flattenTree", () => {
  it("flattens tree with correct depth values", () => {
    const spans: Span[] = [
      makeSpan({ spanId: "root", parentSpanId: "", startTimeUs: 1000 }),
      makeSpan({ spanId: "child", parentSpanId: "root", startTimeUs: 2000 }),
      makeSpan({
        spanId: "grandchild",
        parentSpanId: "child",
        startTimeUs: 3000,
      }),
    ];

    const tree = buildSpanTree(spans);
    const flat = flattenTree(tree);

    expect(flat).toHaveLength(3);
    expect(flat[0].span.spanId).toBe("root");
    expect(flat[0].depth).toBe(0);
    expect(flat[1].span.spanId).toBe("child");
    expect(flat[1].depth).toBe(1);
    expect(flat[2].span.spanId).toBe("grandchild");
    expect(flat[2].depth).toBe(2);
  });

  it("produces correct order for siblings", () => {
    const spans: Span[] = [
      makeSpan({ spanId: "root", parentSpanId: "", startTimeUs: 1000 }),
      makeSpan({
        spanId: "child-b",
        parentSpanId: "root",
        startTimeUs: 3000,
      }),
      makeSpan({
        spanId: "child-a",
        parentSpanId: "root",
        startTimeUs: 2000,
      }),
    ];

    const tree = buildSpanTree(spans);
    const flat = flattenTree(tree);

    expect(flat).toHaveLength(3);
    expect(flat[0].span.spanId).toBe("root");
    expect(flat[1].span.spanId).toBe("child-a");
    expect(flat[2].span.spanId).toBe("child-b");
  });
});

describe("formatDuration", () => {
  it("formats microseconds", () => {
    expect(formatDuration(500)).toBe("500µs");
  });

  it("formats milliseconds", () => {
    expect(formatDuration(1500)).toBe("1.5ms");
    expect(formatDuration(150_000)).toBe("150.0ms");
  });

  it("formats seconds", () => {
    expect(formatDuration(2_500_000)).toBe("2.50s");
  });
});

describe("durationColor", () => {
  it("returns green for < 100ms", () => {
    expect(durationColor(50_000)).toBe("#22c55e");
  });

  it("returns yellow for < 1s", () => {
    expect(durationColor(500_000)).toBe("#eab308");
  });

  it("returns red for >= 1s", () => {
    expect(durationColor(1_000_000)).toBe("#ef4444");
    expect(durationColor(5_000_000)).toBe("#ef4444");
  });
});

describe("statusColor", () => {
  it("returns red for error", () => {
    expect(statusColor("error")).toBe("#ef4444");
    expect(statusColor("ERROR")).toBe("#ef4444");
  });

  it("returns green for ok", () => {
    expect(statusColor("ok")).toBe("#22c55e");
    expect(statusColor("")).toBe("#22c55e");
  });
});

describe("truncateId", () => {
  it("truncates long IDs", () => {
    expect(truncateId("abcdef1234567890")).toBe("abcdef12…");
  });

  it("keeps short IDs as-is", () => {
    expect(truncateId("short")).toBe("short");
  });
});

describe("kindClass", () => {
  it("maps known kinds", () => {
    expect(kindClass("server")).toBe("server");
    expect(kindClass("CLIENT")).toBe("client");
    expect(kindClass("producer")).toBe("producer");
    expect(kindClass("consumer")).toBe("consumer");
  });

  it("defaults to internal", () => {
    expect(kindClass("")).toBe("internal");
    expect(kindClass("unknown")).toBe("internal");
  });
});

describe("parseAttributes", () => {
  it("parses valid JSON", () => {
    expect(parseAttributes('{"key":"value"}')).toEqual({ key: "value" });
  });

  it("returns empty for invalid JSON", () => {
    expect(parseAttributes("not json")).toEqual({});
    expect(parseAttributes("")).toEqual({});
    expect(parseAttributes("null")).toEqual({});
  });
});

describe("parseEvents", () => {
  it("parses valid events", () => {
    const events = parseEvents('[{"name":"log","timeUs":1000}]');
    expect(events).toHaveLength(1);
    expect(events[0].name).toBe("log");
  });

  it("returns empty for invalid JSON", () => {
    expect(parseEvents("")).toEqual([]);
    expect(parseEvents("null")).toEqual([]);
  });
});

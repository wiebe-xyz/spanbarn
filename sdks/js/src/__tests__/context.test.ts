import { describe, expect, it } from "vitest";
import {
  injectTraceparent,
  makeTraceparent,
  parseTraceparent,
} from "../context.js";

describe("makeTraceparent", () => {
  it("generates correct W3C traceparent format", () => {
    const traceId = "a".repeat(32);
    const spanId = "b".repeat(16);
    expect(makeTraceparent(traceId, spanId)).toBe(
      `00-${"a".repeat(32)}-${"b".repeat(16)}-01`,
    );
  });
});

describe("parseTraceparent", () => {
  it("parses a valid traceparent header", () => {
    const traceId = "0af7651916cd43dd8448eb211c80319c";
    const spanId = "b7ad6b7169203331";
    const header = `00-${traceId}-${spanId}-01`;
    const result = parseTraceparent(header);
    expect(result).toEqual({ traceId, spanId });
  });

  it("returns null for invalid header — wrong number of parts", () => {
    expect(parseTraceparent("00-abc")).toBeNull();
  });

  it("returns null for invalid header — wrong version", () => {
    expect(
      parseTraceparent(`01-${"a".repeat(32)}-${"b".repeat(16)}-01`),
    ).toBeNull();
  });

  it("returns null for invalid header — bad trace ID length", () => {
    expect(
      parseTraceparent(`00-${"a".repeat(31)}-${"b".repeat(16)}-01`),
    ).toBeNull();
  });

  it("returns null for invalid header — bad span ID length", () => {
    expect(
      parseTraceparent(`00-${"a".repeat(32)}-${"b".repeat(15)}-01`),
    ).toBeNull();
  });

  it("returns null for garbage input", () => {
    expect(parseTraceparent("not-a-valid-traceparent")).toBeNull();
    expect(parseTraceparent("")).toBeNull();
  });
});

describe("injectTraceparent", () => {
  it("injects traceparent into headers object", () => {
    const headers: Record<string, string> = {};
    const traceId = "a".repeat(32);
    const spanId = "b".repeat(16);
    injectTraceparent(headers, traceId, spanId);
    expect(headers["traceparent"]).toBe(`00-${traceId}-${spanId}-01`);
  });
});

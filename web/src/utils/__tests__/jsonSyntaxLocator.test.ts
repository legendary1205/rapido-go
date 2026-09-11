import { describe, expect, it } from "vitest";
import { locateJSONSyntaxError } from "../jsonSyntaxLocator";

// A guard against the exact trap this module exists to avoid: if a future
// engine upgrade makes JSON.parse's own message parseable again, that's fine,
// but this suite must never start silently relying on it - every case here
// asserts the line/column our own scanner computes, independent of whatever
// JSON.parse's error message happens to say on the engine running the tests.

describe("locateJSONSyntaxError", () => {
  it("returns null for valid JSON (caller should never call this on success)", () => {
    expect(locateJSONSyntaxError('{"a": 1, "b": [1, 2, 3]}')).toBeNull();
  });

  it("locates a missing value on line 1", () => {
    const loc = locateJSONSyntaxError('{"a": }');
    expect(loc).not.toBeNull();
    expect(loc!.line).toBe(1);
  });

  it("locates an error on a later line in a multi-line document", () => {
    const text = ['{', '  "a": 1,', '  "b": }', '}'].join("\n");
    const loc = locateJSONSyntaxError(text);
    expect(loc).not.toBeNull();
    expect(loc!.line).toBe(3);
  });

  it("locates a trailing comma just before the closing brace", () => {
    const text = ['{', '  "a": 1,', '}'].join("\n");
    const loc = locateJSONSyntaxError(text);
    expect(loc).not.toBeNull();
    expect(loc!.line).toBe(3);
  });

  it("locates an unterminated string", () => {
    const text = ['{', '  "a": "unterminated', '}'].join("\n");
    const loc = locateJSONSyntaxError(text);
    expect(loc).not.toBeNull();
    expect(loc!.line).toBe(2);
  });

  it("locates a missing closing bracket at end of input", () => {
    const text = ['{', '  "a": [1, 2, 3'].join("\n");
    const loc = locateJSONSyntaxError(text);
    expect(loc).not.toBeNull();
    expect(loc!.line).toBe(2);
  });

  it("locates trailing content after a complete value", () => {
    const text = '{"a": 1} extra';
    const loc = locateJSONSyntaxError(text);
    expect(loc).not.toBeNull();
    expect(loc!.line).toBe(1);
  });

  it("reports line 1 column 1 for an empty document", () => {
    const loc = locateJSONSyntaxError("");
    expect(loc).toEqual({ line: 1, column: 1, message: "Unexpected end of input" });
  });

  it("accepts every JSON primitive shape", () => {
    expect(locateJSONSyntaxError("true")).toBeNull();
    expect(locateJSONSyntaxError("false")).toBeNull();
    expect(locateJSONSyntaxError("null")).toBeNull();
    expect(locateJSONSyntaxError("42")).toBeNull();
    expect(locateJSONSyntaxError("-3.14e10")).toBeNull();
    expect(locateJSONSyntaxError('"a string"')).toBeNull();
    expect(locateJSONSyntaxError("[]")).toBeNull();
    expect(locateJSONSyntaxError("{}")).toBeNull();
  });

  it("rejects a bare unquoted key", () => {
    const loc = locateJSONSyntaxError("{a: 1}");
    expect(loc).not.toBeNull();
    expect(loc!.line).toBe(1);
  });
});

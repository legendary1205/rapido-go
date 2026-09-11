// Finds the 1-based line/column where a JSON string first breaks strict JSON
// grammar, for use ONLY after JSON.parse has already thrown on that string -
// this exists purely to turn "your JSON is invalid" into "line 12 has a
// problem" for an admin hand-editing config.
//
// Deliberately does NOT rely on JSON.parse's own SyntaxError.message: that
// message's format is engine- and version-specific and already unstable in
// practice - confirmed on this exact project's Node toolchain, where V8's
// current format ("Unexpected token '}', ...\"snippet\"... is not valid
// JSON") dropped the "at position N" text older parsers (including the
// npm-maintained json-parse-even-better-errors package) depend on entirely.
// Scanning the text ourselves with our own line/column counters is the only
// approach that can't be broken by a future engine update.
//
// This is a real (if minimal) recursive-descent validator for the strict
// JSON grammar - not a heuristic - so it agrees with JSON.parse on what's
// valid. It is only ever invoked on the error path (JSON.parse already
// failed), never as the primary parser.

export type JSONSyntaxErrorLocation = {
  line: number;
  column: number;
  message: string;
};

const WHITESPACE = new Set([" ", "\t", "\n", "\r"]);
const DIGIT = /[0-9]/;

export const locateJSONSyntaxError = (text: string): JSONSyntaxErrorLocation | null => {
  let i = 0;
  let line = 1;
  let column = 1;
  const len = text.length;

  const advance = () => {
    if (text[i] === "\n") {
      line++;
      column = 1;
    } else {
      column++;
    }
    i++;
  };

  const fail = (message: string): JSONSyntaxErrorLocation => ({ line, column, message });

  const skipWhitespace = () => {
    while (i < len && WHITESPACE.has(text[i])) advance();
  };

  const parseString = (): JSONSyntaxErrorLocation | null => {
    advance(); // opening quote
    while (true) {
      if (i >= len) return fail("Unterminated string");
      const c = text[i];
      if (c === '"') {
        advance();
        return null;
      }
      if (c === "\\") {
        advance();
        if (i >= len) return fail("Unterminated escape sequence");
        const esc = text[i];
        if (esc === "u") {
          advance();
          for (let k = 0; k < 4; k++) {
            if (i >= len || !/[0-9a-fA-F]/.test(text[i])) return fail("Invalid unicode escape");
            advance();
          }
          continue;
        }
        if (!'"\\/bfnrt'.includes(esc)) return fail(`Invalid escape character "\\${esc}"`);
        advance();
        continue;
      }
      if (c.charCodeAt(0) < 0x20) return fail("Invalid control character in string");
      advance();
    }
  };

  const parseNumber = (): JSONSyntaxErrorLocation | null => {
    const start = i;
    if (text[i] === "-") advance();
    if (text[i] === "0") {
      advance();
    } else if (DIGIT.test(text[i] ?? "")) {
      while (i < len && DIGIT.test(text[i])) advance();
    } else {
      return fail("Invalid number");
    }
    if (text[i] === ".") {
      advance();
      if (!DIGIT.test(text[i] ?? "")) return fail("Invalid number");
      while (i < len && DIGIT.test(text[i])) advance();
    }
    if (text[i] === "e" || text[i] === "E") {
      advance();
      if (text[i] === "+" || text[i] === "-") advance();
      if (!DIGIT.test(text[i] ?? "")) return fail("Invalid number");
      while (i < len && DIGIT.test(text[i])) advance();
    }
    return i === start ? fail("Invalid number") : null;
  };

  const parseLiteral = (literal: string): JSONSyntaxErrorLocation | null => {
    for (const ch of literal) {
      if (text[i] !== ch) return fail(`Unexpected token, expected "${literal}"`);
      advance();
    }
    return null;
  };

  const parseValue = (): JSONSyntaxErrorLocation | null => {
    skipWhitespace();
    if (i >= len) return fail("Unexpected end of input");
    const c = text[i];
    if (c === '"') return parseString();
    if (c === "{") return parseObject();
    if (c === "[") return parseArray();
    if (c === "-" || DIGIT.test(c)) return parseNumber();
    if (c === "t") return parseLiteral("true");
    if (c === "f") return parseLiteral("false");
    if (c === "n") return parseLiteral("null");
    return fail(`Unexpected token "${c}"`);
  };

  const parseObject = (): JSONSyntaxErrorLocation | null => {
    advance(); // {
    skipWhitespace();
    if (text[i] === "}") {
      advance();
      return null;
    }
    while (true) {
      skipWhitespace();
      if (text[i] !== '"') return fail("Expected a property name in double quotes");
      const keyErr = parseString();
      if (keyErr) return keyErr;
      skipWhitespace();
      if (text[i] !== ":") return fail('Expected ":" after a property name');
      advance();
      const valueErr = parseValue();
      if (valueErr) return valueErr;
      skipWhitespace();
      if (text[i] === ",") {
        advance();
        continue;
      }
      if (text[i] === "}") {
        advance();
        return null;
      }
      return fail('Expected "," or "}"');
    }
  };

  const parseArray = (): JSONSyntaxErrorLocation | null => {
    advance(); // [
    skipWhitespace();
    if (text[i] === "]") {
      advance();
      return null;
    }
    while (true) {
      const valueErr = parseValue();
      if (valueErr) return valueErr;
      skipWhitespace();
      if (text[i] === ",") {
        advance();
        continue;
      }
      if (text[i] === "]") {
        advance();
        return null;
      }
      return fail('Expected "," or "]"');
    }
  };

  const valueErr = parseValue();
  if (valueErr) return valueErr;
  skipWhitespace();
  return i < len ? fail("Unexpected trailing content after the JSON value") : null;
};

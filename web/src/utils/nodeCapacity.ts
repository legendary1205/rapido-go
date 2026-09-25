// A node's capacity: how many open client connections mean 100% load for it.
// This is the client-side mirror of the rule the panel enforces (a whole number
// in 1..10,000,000, or nothing at all for "use the panel default"), so the
// admin sees the problem next to the field instead of as a rejected save.

export const MIN_NODE_CAPACITY = 1;
export const MAX_NODE_CAPACITY = 10_000_000;

export type CapacityInputError =
  | { kind: "notInteger"; value: string }
  | { kind: "outOfRange"; value: string };

export type CapacityInputResult =
  | { ok: true; capacity: number | null }
  | { ok: false; error: CapacityInputError };

// Persian and Arabic-Indic digits are what a Persian keyboard types by default;
// treating them as the digits they are beats "not a whole number" (same reading
// as the ports field).
const EASTERN_DIGITS: Record<string, string> = {
  "۰": "0", "۱": "1", "۲": "2", "۳": "3", "۴": "4",
  "۵": "5", "۶": "6", "۷": "7", "۸": "8", "۹": "9",
  "٠": "0", "١": "1", "٢": "2", "٣": "3", "٤": "4",
  "٥": "5", "٦": "6", "٧": "7", "٨": "8", "٩": "9",
};

/**
 * Parses the capacity field. Empty means "use the panel default": that is
 * ok, with a null capacity - the value the panel stores as "no capacity of its
 * own" - not an error. Anything that is not a whole number is reported rather
 * than guessed at: "1,5" is not 15.
 */
export const parseCapacityInput = (input: string): CapacityInputResult => {
  const text = input.replace(/[۰-۹٠-٩]/g, (d) => EASTERN_DIGITS[d]).trim();
  if (text === "") return { ok: true, capacity: null };
  if (!/^-?\d+$/.test(text)) return { ok: false, error: { kind: "notInteger", value: input.trim() } };
  const capacity = Number(text);
  if (capacity < MIN_NODE_CAPACITY || capacity > MAX_NODE_CAPACITY) {
    return { ok: false, error: { kind: "outOfRange", value: input.trim() } };
  }
  return { ok: true, capacity };
};

/** The inverse of parseCapacityInput, for seeding the field: null is empty. */
export const formatCapacityInput = (capacity: number | null | undefined): string =>
  capacity == null ? "" : String(capacity);

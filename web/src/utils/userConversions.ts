// Extracted from the old dashboard's UserFormModal.tsx, which inlined these
// exact formulas in its onSubmit/reset handlers. Pulled out verbatim (not
// reinterpreted) so the Users and User Templates forms share one tested
// implementation instead of two copies that could quietly drift apart.

/** Bytes per gibibyte - the unit the "data limit (GB)" field actually means. */
export const GIB = 1024 ** 3;

/** Seconds per day, for converting an "expire in N days" field to a Unix timestamp. */
export const DAY_SECONDS = 86400;

/**
 * Form "data limit (GB)" -> the API's `data_limit` (bytes, or null for
 * unlimited). Mirrors the old UserFormModal.tsx onSubmit exactly: `gb
 * ? Math.round(gb * GIB) : null` - 0, negative-cancels-to-falsy, NaN, and
 * undefined all mean "unlimited", not "zero bytes".
 */
export const gbToDataLimit = (gb: number | null | undefined): number | null =>
  gb ? Math.round(gb * GIB) : null;

/**
 * The API's `data_limit` (bytes) -> the form's "data limit (GB)" field value.
 * Mirrors the old UserFormModal.tsx reset() exactly: a falsy data_limit (null
 * or 0) becomes an empty value, never "0", since 0 GB would read as "no
 * traffic allowed" rather than "unlimited".
 */
export const dataLimitToGb = (
  dataLimit: number | null | undefined
): number | null => (dataLimit ? dataLimit / GIB : null);

/**
 * Form "expire in N days from now" -> the API's `expire` (Unix seconds, or
 * null for never). `now` defaults to the real clock but is overridable so
 * tests are deterministic. Mirrors the old UserFormModal.tsx onSubmit
 * exactly: `days ? Math.floor(now/1000) + days*DAY_SECONDS : null`.
 */
export const daysToExpire = (
  days: number | null | undefined,
  now: number = Date.now()
): number | null => (days ? Math.floor(now / 1000) + days * DAY_SECONDS : null);

/**
 * The API's `expire` (Unix seconds) -> the form's "expire in N days from now"
 * field value. Mirrors the old UserFormModal.tsx reset() exactly: rounds to
 * the nearest day, and can come back negative for a user already expired
 * (the form doesn't clamp this - showing "-3" is the honest state).
 */
export const expireToDays = (
  expire: number | null | undefined,
  now: number = Date.now()
): number | null =>
  expire ? Math.round((expire - now / 1000) / DAY_SECONDS) : null;

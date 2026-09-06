// Extracted from the old dashboard's rapido-ui/Integrations.tsx `save()`,
// which inlined this exact body-construction loop. Must stay byte-for-byte
// aligned with the Go backend's tri-state PATCH semantics
// (internal/httpapi/settings.go's handleUpdateIntegrationSettings, bound as
// map[string]json.RawMessage specifically so it can tell "key omitted" from
// "key present with value null" apart): a key absent from the returned
// object leaves that setting untouched, a key present with value `null`
// clears the DB override back to the .env default, and a key present with a
// real value overwrites it.
export type IntegrationFieldKind =
  | "text"
  | "password"
  | "int"
  | "intArray"
  | "strArray";

export type IntegrationFieldSpec = {
  key: string;
  kind: IntegrationFieldKind;
};

/**
 * Builds the PUT /api/settings/integrations request body from a form's
 * per-field draft strings and the set of fields the admin explicitly chose
 * to reset to the .env value.
 *
 * Precedence is deliberate and checked in this order for every field:
 *  1. Cleared (in `clearedKeys`) -> always `null`, even if a draft value is
 *     also sitting in `drafts` for the same key (a stray, unsaved keystroke
 *     the admin never actually asked to apply must never win over an
 *     explicit "reset to .env" click).
 *  2. A non-empty (post-trim) draft -> parsed per the field's `kind`.
 *  3. Anything else (untouched, or a draft that trims to empty) -> the key
 *     is omitted from the result entirely, so the backend leaves the
 *     current value alone.
 */
export const buildIntegrationsPatch = (
  fields: IntegrationFieldSpec[],
  drafts: Record<string, string>,
  clearedKeys: ReadonlySet<string> | readonly string[]
): Record<string, unknown> => {
  const cleared =
    clearedKeys instanceof Set ? clearedKeys : new Set(clearedKeys);
  const body: Record<string, unknown> = {};

  fields.forEach((field) => {
    if (cleared.has(field.key)) {
      body[field.key] = null;
      return;
    }

    const raw = (drafts[field.key] ?? "").trim();
    if (!raw) return;

    switch (field.kind) {
      case "int":
        body[field.key] = Number(raw);
        break;
      case "intArray":
        body[field.key] = raw
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean)
          .map(Number);
        break;
      case "strArray":
        body[field.key] = raw
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean);
        break;
      default:
        body[field.key] = raw;
    }
  });

  return body;
};

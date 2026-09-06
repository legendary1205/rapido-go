// Promoted out of HostsAdmin.tsx/AdminsAdmin.tsx/InactiveAdmins.tsx/
// Integrations.tsx in the old dashboard, which each carried an identical
// copy of both functions. ofetch throws a FetchError whose `.data` (the
// parsed JSON body) carries `{ detail: ... }` on every error response this
// backend returns - `detail` can be a plain string (most handlers here), or
// occasionally a structured value (Gin's binding-validation errors), so
// detailToText recurses through arrays/objects to always produce something
// readable instead of "[object Object]".
export const detailToText = (detail: unknown): string => {
  if (typeof detail === "string") return detail;
  if (Array.isArray(detail)) {
    return detail.map(detailToText).filter(Boolean).join(" · ");
  }
  if (detail && typeof detail === "object") {
    const r = detail as Record<string, unknown>;
    if (typeof r.msg === "string") return r.msg;
    return Object.entries(r)
      .map(([f, m]) => (detailToText(m) ? `${f}: ${detailToText(m)}` : ""))
      .filter(Boolean)
      .join(" · ");
  }
  return "";
};

// ofetch's FetchError shape isn't exported in a form worth importing just for
// this - `cause` is typed loosely and narrowed defensively instead, so a
// network failure (no `.response._data` at all - blocked request, DNS
// failure, etc.) falls through to `.message` rather than throwing a second,
// uncaught error while trying to read `.detail` off `undefined`.
export const errorText = (cause: unknown, fallback: string): string => {
  const e = cause as
    | { response?: { _data?: { detail?: unknown } }; message?: string }
    | undefined;
  return detailToText(e?.response?._data?.detail) || e?.message || fallback;
};

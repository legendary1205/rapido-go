// Bidi helpers for the Persian UI.
//
// A Latin-script value dropped into a Persian sentence - "1.2 GB", a
// subscription URL, "#1234" - is a left-to-right island inside a right-to-left
// paragraph. The Unicode bidi algorithm mostly gets that right on its own, but
// it resolves *neutral* characters (punctuation, "#", "/", ":") from the
// surrounding context, which is how "#1234" ends up drawn as "1234#" and a
// path like "/sub/abc" loses its leading slash to the far end of the line.
//
// Where the value has an element of its own, the HTML `dir` attribute is the
// right tool - it implies `unicode-bidi: isolate`. These helpers are for the
// other case: a value interpolated into a translated string, where there is no
// element to hang an attribute on and only the characters themselves can carry
// the isolation.

/** U+2066 LEFT-TO-RIGHT ISOLATE */
const LRI = "⁦";
/** U+2069 POP DIRECTIONAL ISOLATE */
const PDI = "⁩";

/**
 * Wrap a value so it is laid out left-to-right and cannot influence - or be
 * influenced by - the direction of the text around it. The markers are
 * default-ignorable, so this is a no-op visually in an LTR page and costs
 * nothing to apply unconditionally.
 */
export const ltrIsolate = (value: string | number): string =>
  `${LRI}${value}${PDI}`;

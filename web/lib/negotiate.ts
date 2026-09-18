// Accept negotiation (R3). A pure, bounded parser: no I/O, no
// dependency on the request beyond the header string, safe to run on
// the Edge runtime from middleware.
//
// Markdown is chosen only when an explicit text/markdown range has
// q > 0 and its q beats the effective q of text/html, where HTML's
// effective q follows explicit text/html, then text/*, then */*. A
// tie goes to whichever range was listed first. Missing, malformed,
// or wildcard-only headers yield HTML.
//
// Bounds: at most 2,048 characters and 32 comma-separated ranges are
// read, in one pass. q is accepted only as 0, 1, or a decimal with
// one to three digits (0.5, 0.125, 1.000); any other q drops the
// range as if it were absent.

export type Representation = "html" | "markdown";

const MAX_CHARS = 2048;
const MAX_RANGES = 32;

// Anchored: `0`, `0.d{1,3}`, `1`, `1.0{1,3}`. Rejects `.5`, `1.5`,
// `0.1234`, and anything non-numeric.
const Q_VALUE = /^(?:0(?:\.\d{1,3})?|1(?:\.0{1,3})?)$/;

// RFC 9110 token characters for type and subtype, lowercased before
// matching. `*` is allowed only as `*/*` or `type/*`.
const MEDIA_RANGE =
  /^(?:\*\/\*|[!#$%&'*+.^_`|~0-9a-z-]+\/(?:\*|[!#$%&'*+.^_`|~0-9a-z-]+))$/;

type Candidate = { q: number; index: number };

type Parsed = { type: string; q: number };

function parseRange(raw: string): Parsed | undefined {
  const parts = raw.split(";");
  const type = parts[0].trim().toLowerCase();
  if (!MEDIA_RANGE.test(type)) return undefined;
  let q = 1;
  for (let i = 1; i < parts.length; i++) {
    const param = parts[i].trim();
    const eq = param.indexOf("=");
    if (eq === -1) continue;
    const name = param.slice(0, eq).trim().toLowerCase();
    if (name !== "q") continue;
    const value = param.slice(eq + 1).trim();
    if (!Q_VALUE.test(value)) return undefined;
    q = Number(value);
    // The first q parameter is the one that counts; accept-ext
    // parameters after it are ignored.
    break;
  }
  return { type, q };
}

export function preferredRepresentation(
  accept: string | null,
): Representation {
  if (accept === null || accept.length === 0) return "html";
  const input = accept.length > MAX_CHARS ? accept.slice(0, MAX_CHARS) : accept;

  let markdown: Candidate | undefined;
  let html: Candidate | undefined;
  let textAny: Candidate | undefined;
  let any: Candidate | undefined;

  let index = 0;
  let start = 0;
  while (start <= input.length && index < MAX_RANGES) {
    let end = input.indexOf(",", start);
    if (end === -1) end = input.length;
    const parsed = parseRange(input.slice(start, end));
    start = end + 1;
    const position = index++;
    if (!parsed) continue;
    const candidate = { q: parsed.q, index: position };
    // The first listing of a given range wins; later duplicates are
    // ignored so ordering stays the tie-breaker.
    switch (parsed.type) {
      case "text/markdown":
        markdown ??= candidate;
        break;
      case "text/html":
        html ??= candidate;
        break;
      case "text/*":
        textAny ??= candidate;
        break;
      case "*/*":
        any ??= candidate;
        break;
    }
  }

  if (!markdown || markdown.q === 0) return "html";
  const effectiveHtml = html ?? textAny ?? any;
  if (!effectiveHtml) return "markdown";
  if (markdown.q > effectiveHtml.q) return "markdown";
  if (markdown.q === effectiveHtml.q && markdown.index < effectiveHtml.index) {
    return "markdown";
  }
  return "html";
}

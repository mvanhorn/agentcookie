// /developers copy. Plain data rendered by the HTML page through
// TrustArticle and re-emitted by the Markdown twin, like the other
// trust pages. Every fact traces to the Go source (internal/cli/sink.go,
// internal/pairing/pairing.go, internal/protocol/envelope.go,
// internal/transport/crypto.go) or to docs/quickstart.md; the port
// numbers and MagicDNS hostnames are the quickstart's. No email, no
// exclamation marks, no invented service.

import { LINKS, RELEASES, GITHUB } from "./links";
import { SITE_ORIGIN } from "../site";
import type { TrustPage } from "./trust";

export const OPENAPI_URL = `${SITE_ORIGIN}/openapi.json`;
export const API_CATALOG_URL = `${SITE_ORIGIN}/.well-known/api-catalog`;

export const DEVELOPERS: TrustPage = {
  key: "developers",
  title: "Developers",
  description:
    "The sink HTTP interface: three endpoints served by the agentcookie binary on your own tailnet, an OpenAPI description of them, and why nothing is hosted at agentcookie.dev.",
  sections: [
    {
      heading: "What the sink interface is and where it runs",
      paragraphs: [
        "agentcookie has one HTTP surface, and it belongs to the copy of the binary you run. `agentcookie sink` starts a plain HTTP listener on the machine that receives sessions (a Linux box or a second Mac), bound to that machine's Tailscale 100.x address on the port in its listen.addr config value, 9999 in the quickstart. The source Mac POSTs a sealed envelope to it every time Chrome's cookie store or a registered secrets file changes. A second, short-lived listener exists only during pairing: `agentcookie pair --as source` listens on port 9998 for at most ten minutes so the sink can complete a key exchange, then exits.",
        "Both listeners refuse to bind anything but a tailnet-private address or explicit loopback, so the only clients that can reach them are devices on your tailnet. Address them by MagicDNS name, for example my-sink.tailnet.ts.net, rather than a frozen 100.x IP; the source resolves the name through tailscale status at sync time, so a Tailscale re-auth that changes the IP does not break sync.",
      ],
    },
    {
      heading: "There is no hosted API",
      paragraphs: [
        "agentcookie.dev is a static site. It has no API, no API key, no sign-up, no account, and nothing to call. The tool never contacts this domain either: there is no relay, no telemetry, and no cloud component, and the maintainer cannot see what you sync. Requests under /api on this domain answer a JSON 404 that says so; the errors section below has the shape. The OpenAPI document published here describes the interface of the software you run yourself, and the servers it lists are templates you fill in with your own hostnames.",
      ],
    },
    {
      heading: "The three endpoints",
      paragraphs: [
        "GET /healthz on the sink returns the single line ok. It proves the process is up and bound; it says nothing about pairing state or Chrome.",
        "POST /sync on the sink takes the raw bytes of an AES-256-GCM seal (a 12-byte nonce followed by ciphertext and tag) over a JSON envelope of cookies, optional localStorage and IndexedDB tarballs, and optional per-CLI secrets. The sink answers 200 with a one-line summary, 401 when the seal does not open, 409 when the sequence number is not newer than the last one it accepted from that source (replay defense), 400 for an unreadable or wrong-version envelope, 405 for anything but POST, and 500 when the write fails.",
        "POST /pair on the source's pairing listener takes a JSON body with the pairing code, the sink's ephemeral X25519 public key, and its hostname, and answers with the source's public key, its hostname, and a fingerprint of the derived key. A wrong code gets 401; repeated wrong codes from one address get 429; the listener shuts down after the first successful handshake.",
        "A liveness check from any machine on the same tailnet, with the sink's MagicDNS name in place of the example:",
      ],
      code: ["$ curl http://my-sink.tailnet.ts.net:9999/healthz", "ok"],
    },
    {
      heading: "How pairing establishes trust",
      paragraphs: [
        "There is no bearer token and no password, which is why the OpenAPI document declares an empty security requirement on every operation. Trust comes from two layers. The tailnet decides who can open a connection at all. Then pairing gives each source-sink pair its own key: the source prints a 12-character code, the sink POSTs its ephemeral X25519 public key together with that code, the source replies with its own public key, and both sides run HKDF-SHA256 over the X25519 shared secret with the code as the salt. The code is the man-in-the-middle defense: a party that intercepts the exchange without it derives a different key, and every later /sync seal fails its authentication tag. Both sides print the same fingerprint so you can confirm the match by eye.",
        "After pairing, every /sync body is sealed with AES-256-GCM under the derived key. The sink rejects anything it cannot open, and a persistent sequence tracker rejects replays across restarts. Cookie policy runs on both sides, so the sink independently decides what it will accept even from a paired source.",
      ],
    },
    {
      heading: "Machine-readable descriptions",
      paragraphs: [
        `The OpenAPI 3.1 document at ${OPENAPI_URL} describes the three endpoints with request and response shapes transcribed from the Go types. The RFC 9727 API catalog at ${API_CATALOG_URL} points at that document and at this page. Both are served with open CORS and an ETag so a tool can fetch and cache them. Wire details beyond what the document covers live in the repository: the quickstart for setup, the secrets bus specification for the secrets payload, and the releases page for signed binaries.`,
      ],
    },
    {
      id: "error-not-found",
      heading: "Errors: 404 under /api",
      paragraphs: [
        "Every request to /api or any path beneath it on agentcookie.dev, with any method, answers HTTP 404 with an application/problem+json body (RFC 9457). Its type is the URL of this section, its title is Not Found, and its detail reads: agentcookie.dev hosts no API; the sink HTTP interface runs on your tailnet. The instance member is the request path without the query string, and a links member carries the URLs of the OpenAPI document and this page. Nothing from the request other than that path is echoed. If a client reached that response expecting an agentcookie service, point it at your own sink's tailnet hostname instead.",
      ],
    },
    {
      id: "error-method-not-allowed",
      heading: "Errors: 405 on the documents",
      paragraphs: [
        "The OpenAPI document and the API catalog are read-only. GET, HEAD, and OPTIONS succeed; POST, PUT, PATCH, and DELETE answer HTTP 405 with the same problem+json shape, an Allow header naming the three permitted methods, and the URL of this section as the type.",
      ],
    },
  ],
  links: [
    { label: "OpenAPI document", href: OPENAPI_URL },
    { label: "API catalog (RFC 9727)", href: API_CATALOG_URL },
    { label: "Quickstart", href: LINKS.quickstart },
    { label: "Secrets bus v1 spec", href: LINKS.secretsBusV1Spec },
    { label: "Releases", href: RELEASES },
    { label: "Repository", href: GITHUB },
  ],
};

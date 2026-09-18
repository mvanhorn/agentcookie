// /llms.txt (R12). The body is a pure render of constants; the
// handler only sets the content type. Static: nothing here reads the
// request.

import { renderLlmsTxt } from "@/lib/content/llms";

export const dynamic = "force-static";

export function GET(): Response {
  return new Response(renderLlmsTxt(), {
    status: 200,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}

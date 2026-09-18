# agentcookie web

Marketing site for agentcookie. Deploys to `https://agentcookie.dev` via the
`agentcookie` Vercel project in the `mvanhorns-projects` team.

Stack mirrors `agentcla-web`: Next.js 15, React 19, Tailwind v4, TypeScript,
pnpm. Dark surface, server components only, CSS-only animations.

## Local

```
pnpm install
pnpm dev
```

## Build + test

```
pnpm test     # vitest run
pnpm build    # next build
pnpm lint     # next lint
pnpm typecheck
```

## Source of truth

The page content traces to the agentcookie `README.md` at the repo root. The
hero tagline, the terminal triptych, the secrets-bus tile, and every Working-
list feature card lift from the README. `app/(marketing)/page.test.tsx`
asserts on the specific README-derived strings, so a README rewrite that
changes them will fail the suite until the site catches up.

All homepage copy lives in `lib/content/` (`home.ts`, `features.ts`,
`faq.ts`, `links.ts`); components read text from those modules.
`lib/content/content.test.ts` asserts that every string in the modules
reaches the rendered page. To add or remove a feature card, edit
`lib/content/features.ts` - the tests assert every entry renders.

## Deploy

`vercel --prod --yes` from this directory (the project is already linked via
`.vercel/`). The Vercel project's root directory is set to `web/`, so pushes
to `main` that touch only Go source or `docs/` do not redeploy.

The custom domain `agentcookie.dev` is attached at the project level; the
preview URL is `https://agentcookie.vercel.app`.

# agentcookie web

Single self-contained marketing page for the project. No build step, no JS dependencies, no runtime.

## Local preview

```
cd web
python3 -m http.server 8000
open http://localhost:8000
```

## Deploy to Vercel

```
cd web
npx vercel
```

The page is one HTML file with inlined CSS, so any static host works (Vercel, Netlify, GitHub Pages, S3, your own nginx).

## To register the domain

Pick from candidates:
- agentcookie.dev (preferred)
- agentcookie.com
- cookieclone.dev

Buy from any registrar, point DNS at your static host, ship.

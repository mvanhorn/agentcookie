import type { Config } from "tailwindcss";

// Tailwind v4 prefers CSS-first configuration via `@theme` blocks in
// app/globals.css. Color, font, and spacing tokens live there. This
// config file remains so `next lint` and CI tools that look for it
// resolve a real module — and so future devs have one obvious place
// to anchor content paths.
const config: Config = {
  content: [
    "./app/**/*.{ts,tsx,mdx}",
    "./components/**/*.{ts,tsx,mdx}",
    "./lib/**/*.{ts,tsx}",
  ],
  theme: {
    extend: {},
  },
  plugins: [],
};

export default config;

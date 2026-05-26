import { defineConfig } from "vitest/config";
import path from "node:path";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "node",
    include: [
      "app/**/*.test.ts",
      "app/**/*.test.tsx",
      "lib/**/*.test.ts",
      "lib/**/*.test.tsx",
      "components/**/*.test.ts",
      "components/**/*.test.tsx",
    ],
    globals: false,
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "."),
      // Stub the next.js `server-only` barrier in tests. In production it
      // throws on import from a client component; in vitest's node env
      // there is no client/server boundary, so a no-op module is correct.
      "server-only": path.resolve(__dirname, "tests/shims/server-only.ts"),
    },
  },
});

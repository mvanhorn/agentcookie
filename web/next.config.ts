import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,
  typedRoutes: true,
  // Floor for every page response (R6): the CDN must key HTML on
  // Accept because middleware serves Markdown for the same URL, and
  // no response may be sniffed into another type. Next appends to
  // Vary rather than overwriting, so its own values survive.
  async headers() {
    return [
      {
        source: "/((?!_next/).*)",
        headers: [
          { key: "Vary", value: "Accept" },
          { key: "X-Content-Type-Options", value: "nosniff" },
        ],
      },
    ];
  },
};

export default nextConfig;

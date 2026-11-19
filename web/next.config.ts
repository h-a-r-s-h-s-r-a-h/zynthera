import type { NextConfig } from "next";

// Where the Go API listens. The browser never talks to it directly: every
// /api request goes through this rewrite, so the API is same-origin and needs
// no CORS setup.
const apiUrl = (process.env.API_INTERNAL_URL ?? "http://127.0.0.1:8080").replace(/\/+$/, "");

const nextConfig: NextConfig = {
  cacheComponents: true,
  partialPrefetching: true,
  // Compression would buffer the Server-Sent Events stream on /api/events.
  // In production Caddy compresses responses instead.
  compress: false,
  // Lets the production Docker image ship without node_modules.
  output: "standalone",
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
  async rewrites() {
    return [{ source: "/api/:path*", destination: `${apiUrl}/api/:path*` }];
  },
};

export default nextConfig;

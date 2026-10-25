import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { msw } from "msw/vite";
import { defineConfig } from "vite";

export default defineConfig({
  // msw() only serves the worker script; mocks turn on with VITE_MOCKS=1 (src/main.tsx).
  plugins: [react(), tailwindcss(), msw()],
  resolve: {
    alias: { "@": path.resolve(import.meta.dirname, "./src") },
  },
  server: {
    // The local gateway on :8080 by default. VITE_API_TARGET points the dev
    // server at a deployed backend instead, so frontend work needs no Docker.
    proxy: {
      "/api": {
        target: process.env.VITE_API_TARGET ?? "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
});

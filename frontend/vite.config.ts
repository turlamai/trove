import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

// Stable output names: the Unraid plugin serves the bundle from its own
// plugin directory (cache busting via ?v=<version> query, no hashed names).
export default defineConfig({
  plugins: [preact()],
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
    modulePreload: false,
    rollupOptions: {
      output: {
        entryFileNames: "app.js",
        chunkFileNames: "chunks/[name].js",
        assetFileNames: (info) =>
          info.names?.[0]?.endsWith(".css") ? "app.css" : "assets/[name][extname]",
      },
    },
  },
  server: {
    port: 5183,
    proxy: {
      // dev daemon: troved --dev-tcp 127.0.0.1:8787 --dev-roots ./devroot
      "/api": "http://127.0.0.1:8787",
    },
  },
});

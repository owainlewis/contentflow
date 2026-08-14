import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { fileURLToPath, URL } from "node:url";

export default defineConfig({
  root: fileURLToPath(new URL(".", import.meta.url)),
  plugins: [react()],
  build: {
    outDir: fileURLToPath(new URL("../api/web/dist", import.meta.url)),
    emptyOutDir: false,
  },
  server: {
    host: "127.0.0.1",
    port: 3000,
  },
});

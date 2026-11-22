import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/",
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/v1": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
      "/install.sh": "http://127.0.0.1:8080",
      "/install.ps1": "http://127.0.0.1:8080",
      "/agents": "http://127.0.0.1:8080",
    },
  },
});

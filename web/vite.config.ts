import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// En desarrollo, la API la sirve el servidor Go en su puerto por defecto.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8420",
    },
  },
  build: {
    outDir: "dist",
    // Vacía dist para no incrustar versiones viejas; el script de build vuelve
    // a crear dist/.gitkeep, que go:embed necesita sin panel compilado.
    emptyOutDir: true,
    sourcemap: false,
  },
});

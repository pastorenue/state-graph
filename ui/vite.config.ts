import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';

const apiTarget = process.env.VITE_API_TARGET ?? 'http://localhost:8080';
const wsTarget = apiTarget.replace(/^http/, 'ws');

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  server: {
    proxy: {
      '/api/v1/ws/logs': {
        target: wsTarget,
        ws: true,
        changeOrigin: true,
      },
      '/api/v1/ws': {
        target: wsTarget,
        ws: true,
        changeOrigin: true,
      },
      '/api': {
        target: apiTarget,
        changeOrigin: true,
      },
    },
  },
});

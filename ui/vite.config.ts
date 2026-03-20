import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  server: {
    proxy: {
      '/api/v1/ws': {
        target: process.env.VITE_API_TARGET ?? 'ws://localhost:8080',
        ws: true,
        changeOrigin: true,
      },
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
});

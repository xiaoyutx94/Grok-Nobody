import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': resolve(__dirname, 'src') } },
  server: {
    port: 5179,
    proxy: {
      '/api': 'http://127.0.0.1:17890',
      '/health': 'http://127.0.0.1:17890'
    }
  },
  build: { outDir: 'dist', emptyOutDir: true }
})

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { readFileSync } from 'node:fs'

// 版本标识注入：与 main.go/Cargo/tauri.conf 统一管理的 package.json version
const pkg = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf-8')) as { version: string }

export default defineConfig({
  plugins: [react(), tailwindcss()],
  clearScreen: false,
  base: './',
  define: {
    __APP_VERSION__: JSON.stringify(pkg.version),
  },
  server: {
    // headless 开发：浏览器访问 vite dev server 时 /api 转发到本地管理服务
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:19090',
        changeOrigin: true,
      },
    },
  },
})

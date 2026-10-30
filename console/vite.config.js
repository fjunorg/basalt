import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// base './' + hash 路由：产物嵌入 Go 二进制后在任意挂载路径下可用，
// 且无需服务端 history fallback（私有化/离线约束下的最稳形态）。
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8080' },
  },
})

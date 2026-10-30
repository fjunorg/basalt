import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

// base './' + hash 路由：产物嵌入 Go 二进制后在任意挂载路径下可用，
// 且无需服务端 history fallback（私有化/离线约束下的最稳形态）。
// Element Plus 按需导入：模板组件由 Components+ElementPlusResolver
// 自动注入（组件 JS + 样式）；命令式 API（ElMessage/ElMessageBox）与
// v-loading 指令不走 resolver，见 src/element.ts 与 main.ts 手动注册。
export default defineConfig({
  plugins: [
    vue(),
    Components({ resolvers: [ElementPlusResolver({ importStyle: 'css' })] }),
  ],
  base: './',
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8080' },
  },
})

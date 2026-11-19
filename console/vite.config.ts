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
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // 生产加固：移除调试语句，防止开发者遗留 console 泄漏敏感信息。
    // Vite 6 运行时仍消费 build.esbuild，但类型定义未导出该字段，
    // 故用 @ts-expect-error 绕过；drop 仅作用于生产构建的 JS/TS 产物。
    // @ts-expect-error Vite 6 runtime supports esbuild in build options
    esbuild: { drop: ['console', 'debugger'] },
    minify: 'esbuild',
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8080' },
  },
})

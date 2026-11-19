<template>
  <el-config-provider :locale="zhCn">
    <router-view />
  </el-config-provider>
</template>

<script setup lang="ts">
// 按需导入模式下 locale 经 ElConfigProvider 注入（等价全量时代的
// app.use(ElementPlus, { locale })），组件由 resolver 自动导入。
import zhCn from 'element-plus/es/locale/lang/zh-cn'
</script>

<style>
html, body, #app { height: 0; margin: 0; }
#app { min-height: 100vh; }
/* 页面容器：间距由 Layout 内容区统一提供 */
.page { padding: 0; }
.toolbar {
  margin-bottom: 16px;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
/* 栅格内卡片在窄屏堆叠时保留间距 */
@media (max-width: 992px) {
  .el-col > .el-card { margin-bottom: 16px; }
}
</style>

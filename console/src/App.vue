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
.page { padding: 16px; }
.toolbar { margin-bottom: 12px; display: flex; gap: 8px; }
</style>

<template>
  <div class="page">
    <div class="toolbar">
      <el-button @click="load">刷新</el-button>
      <el-select v-model="module" clearable placeholder="按模块过滤" style="width: 180px" @change="load">
        <el-option v-for="m in modules" :key="m" :value="m" :label="m" />
      </el-select>
      <div style="flex: 1"></div>
      <el-tag :type="verify?.ok ? 'success' : 'danger'" size="large">
        链校验：{{ verify?.ok ? '完整' : '断裂 @ ' + verify?.break_at }} · {{ verify?.total_events ?? 0 }} 事件
      </el-tag>
    </div>
    <el-table :data="events" border stripe v-loading="loading" size="small">
      <el-table-column prop="time" label="时间" width="180">
        <template #default="{ row }">{{ new Date(row.time).toLocaleString('zh-CN') }}</template>
      </el-table-column>
      <el-table-column prop="module" label="模块" width="110" />
      <el-table-column prop="event" label="事件" width="170" />
      <el-table-column prop="actor_id" label="操作者" width="130" show-overflow-tooltip />
      <el-table-column prop="resource" label="对象" width="130" show-overflow-tooltip />
      <el-table-column prop="detail" label="详情" show-overflow-tooltip>
        <template #default="{ row }">{{ detailOf(row) }}</template>
      </el-table-column>
      <el-table-column label="哈希" width="150">
        <template #default="{ row }"><code style="font-size: 11px">{{ row.hash?.slice(0, 16) }}…</code></template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'

const events = ref([])
const verify = ref(null)
const loading = ref(false)
const module = ref('')
const modules = ['identity', 'tenant', 'workflow', 'ai-gateway', 'audit']

const detailOf = (row) => {
  try {
    return row.detail ? JSON.stringify(JSON.parse(row.detail)) : ''
  } catch {
    return String(row.detail || '')
  }
}

async function load() {
  loading.value = true
  try {
    const q = module.value ? `?module=${module.value}&limit=100` : '?limit=100'
    const [ev, vf] = await Promise.all([
      api('/api/v1/audit/events' + q),
      api('/api/v1/audit/verify'),
    ])
    events.value = ev.events || []
    verify.value = vf
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

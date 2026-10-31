<template>
  <div class="page">
    <div class="toolbar">
      <el-input v-model="q.module" placeholder="模块（如 identity）" size="small" clearable style="width: 150px" @keyup.enter="search" />
      <el-input v-model="q.user_id" placeholder="用户 ID" size="small" clearable style="width: 200px" @keyup.enter="search" />
      <el-select v-model="q.status" placeholder="状态码段" size="small" clearable style="width: 110px" @change="search">
        <el-option label="2xx 成功" value="2xx" />
        <el-option label="4xx 客户端" value="4xx" />
        <el-option label="5xx 服务端" value="5xx" />
      </el-select>
      <el-date-picker v-model="range" type="datetimerange" size="small" range-separator="→"
        start-placeholder="开始" end-placeholder="结束" style="width: 340px" @change="search" />
      <el-button size="small" @click="search">查询</el-button>
      <span class="hint">全量请求采集（异步 ≤100ms 窗口）；audit 链之外的日常排障面</span>
    </div>
    <el-table :data="rows" border stripe size="small" v-loading="loading">
      <el-table-column label="时间" width="165">
        <template #default="{ row }">{{ new Date(row.time).toLocaleString('zh-CN') }}</template>
      </el-table-column>
      <el-table-column prop="module" label="模块" width="100" />
      <el-table-column label="方法" width="70">
        <template #default="{ row }">
          <el-tag size="small" :type="methodTag(row.method)">{{ row.method }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="path" label="路径" min-width="230" show-overflow-tooltip />
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.status < 400 ? 'success' : row.status < 500 ? 'warning' : 'danger'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="耗时" width="90">
        <template #default="{ row }">{{ row.duration_ms.toFixed(1) }}ms</template>
      </el-table-column>
      <el-table-column prop="user_id" label="用户" width="130" show-overflow-tooltip>
        <template #default="{ row }">{{ row.user_id ? row.user_id.slice(0, 8) : '-' }}</template>
      </el-table-column>
      <el-table-column prop="remote" label="来源 IP" width="120" />
    </el-table>
    <div class="toolbar" style="justify-content: flex-end" v-if="total > pageSize">
      <el-button size="small" :disabled="page <= 1" @click="load(page - 1)">上一页</el-button>
      <span class="hint">{{ page }} / {{ Math.ceil(total / pageSize) }}（{{ total }} 条）</span>
      <el-button size="small" :disabled="page * pageSize >= total" @click="load(page + 1)">下一页</el-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from '../element'
import { api, errMsg, type OpLogRow } from '../api'

// 契约 oplog-api-contract.md §1：GET /oplog/logs（page/page_size + 过滤）
const rows = ref<OpLogRow[]>([])
const loading = ref(false)
const page = ref(1)
const pageSize = 20
const total = ref(0)
const range = ref<[Date, Date] | null>(null)
const q = reactive({ module: '', user_id: '', status: '' })

const methodTag = (m: string) => ({ GET: 'info', POST: 'primary', PATCH: 'warning', PUT: 'warning', DELETE: 'danger' }[m] || 'info')

function search() { load(1) }

async function load(p = 1) {
  loading.value = true
  page.value = p
  try {
    const params = new URLSearchParams({ page: String(p), page_size: String(pageSize) })
    if (q.module) params.set('module', q.module)
    if (q.user_id) params.set('user_id', q.user_id)
    if (q.status) params.set('status', q.status)
    if (range.value) {
      params.set('since', range.value[0].toISOString())
      params.set('until', range.value[1].toISOString())
    }
    const d = await api<{ rows: OpLogRow[]; total: number; page: number; page_size: number }>('/api/v1/oplog/logs?' + params)
    rows.value = d.rows || []
    total.value = d.total || 0
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

onMounted(() => load())
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; }
</style>

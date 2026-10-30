<template>
  <div class="page">
    <div class="toolbar">
      <el-button @click="load">刷新</el-button>
      <span style="color: var(--semi-color-text-2); font-size: 13px">内置任务注册制调度（间隔执行）；分布式调度随微服务形态评估</span>
    </div>
    <el-table :data="rows" border stripe v-loading="loading" size="small">
      <el-table-column prop="id" label="任务 ID" width="140" />
      <el-table-column prop="name" label="名称" width="140" />
      <el-table-column prop="description" label="说明" min-width="180" show-overflow-tooltip />
      <el-table-column label="间隔" width="90">
        <template #default="{ row }">{{ row.interval_sec }}s</template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '运行中' : '停用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="上次结果" min-width="160">
        <template #default="{ row }">
          <el-tag v-if="row.last_err" type="danger" size="small">失败</el-tag>
          <span style="font-size: 12px; margin-left: 4px">{{ row.last_result || '-' }}</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="200" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('job:manage')" size="small" @click="toggle(row)">
            {{ row.enabled ? '停用' : '启用' }}
          </el-button>
          <el-button v-if="can('job:manage')" size="small" type="primary" @click="trigger(row)">立即执行</el-button>
          <el-button size="small" text @click="viewRuns(row)">记录</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="runsDlg" :title="`运行记录（${viewing?.id || ''}）`" width="560">
      <el-timeline>
        <el-timeline-item v-for="(r, i) in runs" :key="i"
          :timestamp="new Date(r.start).toLocaleString('zh-CN')"
          :type="r.err ? 'danger' : 'success'">
          {{ r.err || r.result }}（{{ r.duration }}）
        </el-timeline-item>
      </el-timeline>
      <el-empty v-if="!runs.length" description="暂无执行记录" :image-size="60" />
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

interface JobRow { id: string; name: string; description: string; interval_sec: number; enabled: boolean; last_err?: string; last_result?: string }
interface JobRun { start: string; err?: string; result?: string; duration: string }

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<JobRow[]>([])
const loading = ref(false)
const runsDlg = ref(false)
const viewing = ref<JobRow | null>(null)
const runs = ref<JobRun[]>([])

async function load() {
  loading.value = true
  try {
    rows.value = await api<JobRow[]>('/api/v1/jobs')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function toggle(row: JobRow) {
  try {
    await api(`/api/v1/jobs/${row.id}/status`, {
      method: 'PATCH',
      body: { enabled: !row.enabled },
    })
    ElMessage.success(row.enabled ? '已停用' : '已启用')
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function trigger(row: JobRow) {
  try {
    await api(`/api/v1/jobs/${row.id}/trigger`, { method: 'POST' })
    ElMessage.success('已执行')
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function viewRuns(row: JobRow) {
  viewing.value = row
  runs.value = await api<JobRun[]>(`/api/v1/jobs/${row.id}/runs`).catch(() => [])
  runsDlg.value = true
}

onMounted(load)
</script>

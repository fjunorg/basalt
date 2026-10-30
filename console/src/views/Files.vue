<template>
  <div class="page">
    <div class="toolbar">
      <el-upload :action="null" :http-request="upload" :show-file-list="false" v-if="can('file:upload')">
        <el-button type="primary">上传文件</el-button>
      </el-upload>
      <el-button @click="load">刷新</el-button>
      <span style="color: #909399; font-size: 13px; margin-left: 8px">单文件 ≤ 32MB · 下载强制 attachment</span>
    </div>
    <el-table :data="rows" border stripe v-loading="loading" size="small">
      <el-table-column prop="name" label="文件名" min-width="220" show-overflow-tooltip />
      <el-table-column label="大小" width="110">
        <template #default="{ row }">{{ fmtSize(row.size) }}</template>
      </el-table-column>
      <el-table-column prop="content_type" label="类型" width="180" show-overflow-tooltip />
      <el-table-column prop="created_at" label="上传时间" width="170">
        <template #default="{ row }">{{ new Date(row.created_at).toLocaleString('zh-CN') }}</template>
      </el-table-column>
      <el-table-column label="操作" width="150" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="download(row)">下载</el-button>
          <el-button v-if="can('file:delete')" size="small" type="danger" @click="del(row)">删</el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, token, perms, hasPerm, errMsg, type Envelope } from '../api'

interface FileRow { id: string; name: string; size: number; content_type: string; created_at: string }

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<FileRow[]>([])
const loading = ref(false)

const fmtSize = (n: number) => n > 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MB' : (n / 1024).toFixed(1) + ' KB'

async function load() {
  loading.value = true
  try {
    rows.value = await api<FileRow[]>('/api/v1/files')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function upload({ file, onSuccess, onError }: {
  file: File
  onSuccess?: (response: unknown) => void
  onError?: (err: Error) => void
}) {
  const fd = new FormData()
  fd.append('file', file)
  try {
    const res = await fetch('/api/v1/files', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + token.get() },
      body: fd,
    })
    const env = (await res.json()) as Envelope
    if (!res.ok || env.code !== 0) throw new Error(env.message || '上传失败')
    ElMessage.success('已上传')
    await load()
    onSuccess?.(env)
  } catch (e) {
    ElMessage.error(errMsg(e))
    onError?.(e instanceof Error ? e : new Error(String(e)))
  }
}

function download(row: FileRow) {
  // 统一走 fetch 以携带 Authorization
  fetch(`/api/v1/files/${row.id}/download`, { headers: { Authorization: 'Bearer ' + token.get() } })
    .then((r) => r.blob())
    .then((b) => {
      const url = URL.createObjectURL(b)
      const a2 = document.createElement('a')
      a2.href = url
      a2.download = row.name
      a2.click()
      URL.revokeObjectURL(url)
    })
}

async function del(row: FileRow) {
  try {
    await ElMessageBox.confirm(`删除「${row.name}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/files/${row.id}`, { method: 'DELETE' })
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

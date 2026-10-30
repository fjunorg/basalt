<template>
  <div class="page">
    <div class="toolbar">
      <el-button type="primary" @click="dlg = true">新建 Agent</el-button>
      <el-button @click="load">刷新</el-button>
      <span class="hint">Agent 定义管理（骨架，等待 API 契约对接）</span>
    </div>
    <el-table :data="rows" border stripe size="small" v-loading="loading">
      <el-table-column prop="name" label="名称" width="180" />
      <el-table-column prop="model" label="模型" width="160" />
      <el-table-column label="工具" min-width="200">
        <template #default="{ row }">
          <el-tag v-for="t in row.tools" :key="t" size="small" style="margin: 2px">{{ t }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="updated_at" label="更新时间" width="170">
        <template #default="{ row }">{{ row.updated_at ? new Date(row.updated_at).toLocaleString('zh-CN') : '-' }}</template>
      </el-table-column>
      <el-table-column label="操作" width="150" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" type="danger" @click="del(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '编辑 Agent' : '新建 Agent'" width="560">
      <el-form label-width="90">
        <el-form-item label="名称"><el-input v-model="form.name" placeholder="Agent 名称" /></el-form-item>
        <el-form-item label="模型"><el-input v-model="form.model" placeholder="模型标识（字段以 API 契约为准）" /></el-form-item>
        <el-form-item label="系统提示"><el-input v-model="form.system_prompt" type="textarea" :rows="4" /></el-form-item>
        <el-form-item label="工具集">
          <el-input v-model="form.tools" placeholder="逗号分隔的工具名（字段以 API 契约为准）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" disabled title="等待 API 契约对接后启用">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { ElMessage } from '../element'

// 骨架阶段行结构为前端预期占位，字段以 agent-api-contract.md 落地后为准
interface AgentDefRow {
  id: string
  name: string
  model: string
  tools: string[]
  status: string
  updated_at?: string
}

const rows = ref<AgentDefRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const editing = ref<AgentDefRow | null>(null)
const form = reactive({ name: '', model: '', system_prompt: '', tools: '' })

async function load() {
  // TODO(contract): GET 端点与响应结构待 agent-api-contract.md 确认后对接
  loading.value = false
}

function openEdit(row: AgentDefRow) {
  editing.value = row
  form.name = row.name
  form.model = row.model
  form.tools = (row.tools || []).join(', ')
  dlg.value = true
}

async function del(_row: AgentDefRow) {
  // TODO(contract): DELETE 端点待契约确认后对接
  ElMessage.info('等待 API 契约对接后启用')
}
</script>

<style scoped>
.hint { color: #909399; font-size: 13px; margin-left: 8px; }
</style>

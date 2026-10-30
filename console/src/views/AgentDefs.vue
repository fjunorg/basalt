<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('agent:manage')" type="primary" @click="openCreate">新建 Agent</el-button>
      <el-button @click="load">刷新</el-button>
    </div>
    <el-table :data="rows" border stripe size="small" v-loading="loading">
      <el-table-column prop="name" label="名称" width="160" />
      <el-table-column prop="description" label="描述" min-width="180" show-overflow-tooltip />
      <el-table-column prop="model" label="模型" width="140" />
      <el-table-column label="工具" min-width="180">
        <template #default="{ row }">
          <el-tag v-for="t in row.tools || []" :key="t" size="small" style="margin: 2px">{{ t }}</el-tag>
          <span v-if="!(row.tools || []).length" style="color: #909399">纯对话</span>
        </template>
      </el-table-column>
      <el-table-column label="温度/轮数" width="100">
        <template #default="{ row }">{{ row.temperature ?? '-' }} / {{ row.max_tool_rounds ?? '-' }}</template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="updated_at" label="更新时间" width="165">
        <template #default="{ row }">{{ row.updated_at ? new Date(row.updated_at).toLocaleString('zh-CN') : '-' }}</template>
      </el-table-column>
      <el-table-column label="操作" width="150" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('agent:manage')" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button v-if="can('agent:manage')" size="small" type="danger" @click="del(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '编辑 Agent' : '新建 Agent'" width="620">
      <el-form label-width="100">
        <el-form-item label="名称" required><el-input v-model="form.name" placeholder="1-128 字符" /></el-form-item>
        <el-form-item label="描述"><el-input v-model="form.description" placeholder="≤255 字符" /></el-form-item>
        <el-form-item label="系统提示"><el-input v-model="form.system_prompt" type="textarea" :rows="4" placeholder="空则使用内置默认" /></el-form-item>
        <el-form-item label="模型" required><el-input v-model="form.model" placeholder="provider/model 或裸模型名" /></el-form-item>
        <el-form-item label="工具">
          <el-select v-model="form.tools" multiple filterable style="width: 100%" placeholder="从注册表选择（空 = 纯对话）">
            <el-option v-for="t in toolOptions" :key="t.name" :value="t.name" :label="t.name">
              <span>{{ t.name }}</span>
              <span v-if="t.description" style="color: #909399; font-size: 12px; margin-left: 8px">{{ t.description }}</span>
            </el-option>
          </el-select>
        </el-form-item>
        <el-form-item label="温度">
          <el-input-number v-model="form.temperature" :min="0" :max="2" :step="0.1" />
        </el-form-item>
        <el-form-item label="工具轮数上限">
          <el-input-number v-model="form.max_tool_rounds" :min="1" :max="10" />
        </el-form-item>
        <el-form-item label="状态">
          <el-radio-group v-model="form.status">
            <el-radio value="active">启用</el-radio>
            <el-radio value="disabled">停用</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg, type AgentDef, type AgentTool } from '../api'

// 页面级 agent:read（路由 meta.perms）；写操作按钮级 agent:manage
const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<AgentDef[]>([])
const toolOptions = ref<AgentTool[]>([])
const loading = ref(false)
const dlg = ref(false)
const saving = ref(false)
const editing = ref<AgentDef | null>(null)
const form = reactive({
  name: '',
  description: '',
  system_prompt: '',
  model: '',
  tools: [] as string[],
  temperature: 0.7,
  max_tool_rounds: 5,
  status: 'active',
})

async function load() {
  loading.value = true
  try {
    const d = await api<{ count: number; items: AgentDef[] }>('/api/v1/agent/agents')
    rows.value = d.items || []
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function loadTools() {
  // §5 工具注册表动态渲染（不硬编码）
  const d = await api<{ tools: AgentTool[] }>('/api/v1/agent/tools').catch(() => ({ tools: [] }))
  toolOptions.value = d.tools || []
}

function resetForm() {
  Object.assign(form, {
    name: '', description: '', system_prompt: '', model: '',
    tools: [], temperature: 0.7, max_tool_rounds: 5, status: 'active',
  })
}

function openCreate() {
  editing.value = null
  resetForm()
  dlg.value = true
}

function openEdit(row: AgentDef) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    description: row.description || '',
    system_prompt: row.system_prompt || '',
    model: row.model,
    tools: [...(row.tools || [])],
    temperature: row.temperature ?? 0.7,
    max_tool_rounds: row.max_tool_rounds ?? 5,
    status: row.status,
  })
  dlg.value = true
}

async function save() {
  if (!form.name || !form.model) {
    ElMessage.warning('名称与模型必填')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      // §2.4 PATCH 传可变字段子集（未传字段不动）
      await api(`/api/v1/agent/agents/${editing.value.id}`, { method: 'PATCH', body: { ...form } })
    } else {
      await api('/api/v1/agent/agents', { method: 'POST', body: { ...form } })
    }
    ElMessage.success('已保存')
    dlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function del(row: AgentDef) {
  try {
    await ElMessageBox.confirm(`删除 Agent「${row.name}」？将级联删除其运行记录与消息历史。`, '确认', { type: 'warning' })
    await api(`/api/v1/agent/agents/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(() => { load(); loadTools() })
</script>

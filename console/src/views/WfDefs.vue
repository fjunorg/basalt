<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('workflow:def:manage')" type="primary" @click="openCreate">新建流程定义</el-button>
      <el-button @click="load">刷新</el-button>
    </div>
    <el-table :data="rows" border stripe v-loading="loading" size="small">
      <el-table-column prop="key" label="Key" width="130" />
      <el-table-column prop="name" label="名称" width="160" />
      <el-table-column prop="category" label="分类" width="90" />
      <el-table-column label="步骤" min-width="260">
        <template #default="{ row }">
          <el-tag v-for="(st, i) in row.steps" :key="i" size="small" style="margin: 2px"
            :type="st.mode === 'or-sign' ? 'warning' : 'primary'">
            {{ i + 1 }}. {{ st.name }}（{{ st.mode === 'or-sign' ? '或签' : '会签' }}·{{ st.approvers.length }}人）
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="150" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('workflow:def:manage')" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button v-if="can('workflow:def:manage')" size="small" :type="row.status === 'active' ? 'warning' : 'success'"
            @click="toggle(row)">{{ row.status === 'active' ? '停用' : '启用' }}</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '编辑流程定义' : '新建流程定义'" width="680">
      <el-form label-width="70">
        <el-form-item label="Key"><el-input v-model="form.key" :disabled="!!editing" placeholder="如 contract" /></el-form-item>
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="分类"><el-input v-model="form.category" placeholder="如 finance / biz" /></el-form-item>
      </el-form>
      <el-divider content-position="left">审批步骤（串行执行）</el-divider>
      <div v-for="(st, i) in form.steps" :key="i" style="display: flex; gap: 8px; margin-bottom: 8px; align-items: center">
        <el-input v-model="st.name" placeholder="步骤名" style="width: 140" />
        <el-select v-model="st.approvers" multiple filterable placeholder="审批人" style="flex: 1">
          <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
        </el-select>
        <el-select v-model="st.mode" style="width: 100">
          <el-option label="会签" value="countersign" />
          <el-option label="或签" value="or-sign" />
        </el-select>
        <el-button text type="danger" @click="form.steps.splice(i, 1)">删</el-button>
      </div>
      <el-button size="small" @click="form.steps.push({ name: '', approvers: [], mode: 'countersign' })">+ 添加步骤</el-button>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

interface WfStep { name: string; approvers: string[]; mode: 'countersign' | 'or-sign' }
interface WfDefRow { id: string; key: string; name: string; category: string; steps: WfStep[]; status: string }
interface UserRow { id: string; username: string }

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<WfDefRow[]>([])
const users = ref<UserRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const editing = ref<WfDefRow | null>(null)
const form = reactive<{ key: string; name: string; category: string; steps: WfStep[] }>({
  key: '', name: '', category: '', steps: [],
})

async function load() {
  loading.value = true
  try {
    ;[rows.value, users.value] = await Promise.all([
      api<WfDefRow[]>('/api/v1/wf/definitions'),
      api<UserRow[]>('/api/v1/identity/users').catch(() => [] as UserRow[]),
    ])
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, { key: '', name: '', category: '', steps: [{ name: '', approvers: [], mode: 'countersign' }] })
  dlg.value = true
}

function openEdit(row: WfDefRow) {
  editing.value = row
  Object.assign(form, { key: row.key, name: row.name, category: row.category, steps: JSON.parse(JSON.stringify(row.steps)) as WfStep[] })
  dlg.value = true
}

async function save() {
  if (!form.key || !form.name || !form.steps.length) {
    ElMessage.warning('Key、名称与至少一个步骤必填')
    return
  }
  for (const st of form.steps) {
    if (!st.approvers.length) {
      ElMessage.warning(`步骤「${st.name || '?'}」需选择审批人`)
      return
    }
  }
  try {
    if (editing.value) {
      await api(`/api/v1/wf/definitions/${editing.value.id}`, {
        method: 'PATCH',
        body: { name: form.name, steps: form.steps },
      })
    } else {
      await api('/api/v1/wf/definitions', { method: 'POST', body: { ...form } })
    }
    ElMessage.success('已保存')
    dlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function toggle(row: WfDefRow) {
  try {
    await api(`/api/v1/wf/definitions/${row.id}`, {
      method: 'PATCH',
      body: { status: row.status === 'active' ? 'disabled' : 'active' },
    })
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

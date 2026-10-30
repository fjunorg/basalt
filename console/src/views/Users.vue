<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('identity:user:create')" type="primary" @click="dlg = true">新建用户</el-button>
      <el-button @click="load">刷新</el-button>
    </div>
    <el-table :data="rows" border stripe v-loading="loading">
      <el-table-column prop="username" label="用户名" width="140" />
      <el-table-column prop="email" label="邮箱" />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'danger'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="tenant_id" label="租户" width="120" show-overflow-tooltip />
      <el-table-column label="角色" width="140">
        <template #default="{ row }">{{ (row.role_ids || []).join(', ') || '-' }}</template>
      </el-table-column>
      <el-table-column prop="created_at" label="创建时间" width="180" :formatter="fmtTime" />
      <el-table-column label="操作" width="220" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('identity:role:manage')" size="small" @click="openRoles(row)">分配角色</el-button>
          <el-button v-if="can('identity:user:status')" size="small" :type="row.status === 'active' ? 'danger' : 'success'" @click="toggle(row)">
            {{ row.status === 'active' ? '停用' : '启用' }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" title="新建用户" width="420">
      <el-form label-width="80">
        <el-form-item label="用户名"><el-input v-model="form.username" /></el-form-item>
        <el-form-item label="密码"><el-input v-model="form.password" type="password" show-password /></el-form-item>
        <el-form-item label="邮箱"><el-input v-model="form.email" /></el-form-item>
        <el-form-item label="租户 ID"><el-input v-model="form.tenant_id" placeholder="留空为平台级用户" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">创建</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="rolesDlg" title="分配角色" width="420">
      <el-select v-model="pickedRoles" multiple style="width: 100%" placeholder="选择角色">
        <el-option v-for="r in roles" :key="r.id" :value="r.id" :label="`${r.code}（${r.name}）`" />
      </el-select>
      <template #footer>
        <el-button @click="rolesDlg = false">取消</el-button>
        <el-button type="primary" @click="saveRoles">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import type { TableColumnCtx } from 'element-plus'
import { api, perms, hasPerm, errMsg } from '../api'

interface UserRow {
  id: string
  username: string
  email: string
  status: string
  tenant_id: string
  role_ids?: string[]
  created_at: string
}
interface RoleRow { id: string; code: string; name: string; permissions: string[] }

// 按钮级权限：与后端通配规则一致
const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<UserRow[]>([])
const roles = ref<RoleRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const saving = ref(false)
const rolesDlg = ref(false)
const pickedRoles = ref<string[]>([])
const editing = ref<UserRow | null>(null)
const form = reactive({ username: '', password: '', email: '', tenant_id: '' })

const fmtTime = (_r: UserRow, _c: TableColumnCtx<UserRow>, v: string) => new Date(v).toLocaleString('zh-CN')

async function load() {
  loading.value = true
  try {
    ;[rows.value, roles.value] = await Promise.all([
      api<UserRow[]>('/api/v1/identity/users'),
      api<RoleRow[]>('/api/v1/identity/roles').catch(() => [] as RoleRow[]),
    ])
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    await api('/api/v1/identity/users', { method: 'POST', body: { ...form } })
    ElMessage.success('已创建')
    dlg.value = false
    Object.assign(form, { username: '', password: '', email: '', tenant_id: '' })
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

function openRoles(row: UserRow) {
  editing.value = row
  pickedRoles.value = [...(row.role_ids || [])]
  rolesDlg.value = true
}

async function saveRoles() {
  if (!editing.value) return
  try {
    await api(`/api/v1/identity/users/${editing.value.id}/roles`, {
      method: 'PUT',
      body: { role_ids: pickedRoles.value },
    })
    ElMessage.success('已保存')
    rolesDlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function toggle(row: UserRow) {
  const to = row.status === 'active' ? 'disabled' : 'active'
  try {
    await api(`/api/v1/identity/users/${row.id}/status`, { method: 'PATCH', body: { status: to } })
    ElMessage.success('已' + (to === 'active' ? '启用' : '停用'))
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

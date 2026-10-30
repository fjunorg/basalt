<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('tenant:create')" type="primary" @click="dlg = true">新建租户</el-button>
      <el-button @click="load">刷新</el-button>
    </div>
    <el-table :data="rows" border stripe v-loading="loading">
      <el-table-column prop="code" label="代码" width="140" />
      <el-table-column prop="name" label="名称" width="200" />
      <el-table-column prop="isolation_mode" label="隔离模式" width="140" />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'danger'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="created_at" label="创建时间" :formatter="fmtTime" />
      <el-table-column label="操作" width="120" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="viewUsers(row)">成员</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" title="新建租户" width="440">
      <el-form label-width="80">
        <el-form-item label="代码"><el-input v-model="form.code" placeholder="如 gov-a" /></el-form-item>
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="隔离模式">
          <el-select v-model="form.isolation_mode" style="width: 100%">
            <el-option label="共享表（行级隔离）" value="shared-schema" />
            <el-option label="共享库独立 schema" value="shared-db" disabled />
            <el-option label="独立库" value="dedicated-db" disabled />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" @click="save">创建</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="usersDlg" :title="`租户成员：${viewing?.name || ''}`" width="560">
      <el-table :data="members" border size="small">
        <el-table-column prop="username" label="用户名" />
        <el-table-column prop="email" label="邮箱" />
        <el-table-column prop="status" label="状态" width="90" />
      </el-table>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from '../element'
import type { TableColumnCtx } from 'element-plus'
import { api, perms, hasPerm, errMsg } from '../api'

interface TenantRow { id: string; code: string; name: string; isolation_mode: string; status: string; created_at: string }
interface UserRow { id: string; username: string; email: string; tenant_id: string; status: string }

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<TenantRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const usersDlg = ref(false)
const viewing = ref<TenantRow | null>(null)
const members = ref<UserRow[]>([])
const form = reactive({ code: '', name: '', isolation_mode: 'shared-schema' })

const fmtTime = (_r: TenantRow, _c: TableColumnCtx<TenantRow>, v: string) => new Date(v).toLocaleString('zh-CN')

async function load() {
  loading.value = true
  try {
    rows.value = await api<TenantRow[]>('/api/v1/tenant/tenants')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function save() {
  try {
    await api('/api/v1/tenant/tenants', { method: 'POST', body: { ...form } })
    ElMessage.success('已创建')
    dlg.value = false
    Object.assign(form, { code: '', name: '', isolation_mode: 'shared-schema' })
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function viewUsers(row: TenantRow) {
  viewing.value = row
  try {
    const all = await api<UserRow[]>(`/api/v1/identity/users`)
    members.value = all.filter((u) => u.tenant_id === row.id)
    usersDlg.value = true
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

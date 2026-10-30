<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('tenant:create')" type="primary" @click="openCreate">新建租户</el-button>
      <el-button @click="load">刷新</el-button>
    </div>
    <el-table :data="rows" border stripe v-loading="loading">
      <el-table-column prop="code" label="代码" width="140" />
      <el-table-column prop="name" label="名称" width="180" />
      <el-table-column label="隔离模式" width="150">
        <template #default="{ row }">
          <el-tag :type="isoTag(row.isolation_mode)" size="small">{{ isoLabel(row.isolation_mode) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="库引用" width="140">
        <template #default="{ row }">
          <span v-if="row.db_ref"><code style="font-size: 12px">{{ row.db_ref }}</code></span>
          <span v-else style="color: #909399">-</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.status === 'active' ? 'success' : 'danger'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="created_at" label="创建时间" :formatter="fmtTime" />
      <el-table-column label="操作" width="170" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('tenant:update')" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" @click="viewUsers(row)">成员</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" title="新建租户" width="480">
      <el-form label-width="90">
        <el-form-item label="代码"><el-input v-model="form.code" placeholder="如 gov-a" /></el-form-item>
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="隔离模式">
          <el-select v-model="form.isolation_mode" style="width: 100%" @change="onModeChange">
            <el-option label="共享表（行级隔离）" value="shared-schema" />
            <!-- API 层校验接受三枚举；shared-db 存储路由未实装（tenant_router 仅
                 dedicated 分支落地），保持不可选避免创建无效模式 -->
            <el-option label="共享库独立 schema（未实装）" value="shared-db" disabled />
            <el-option label="独立库（dedicated-db）" value="dedicated-db" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="form.isolation_mode === 'dedicated-db'" label="库引用" required>
          <el-input v-model="form.db_ref" placeholder="库引用名（如 gov-db），凭据经环境变量注入" />
          <div class="hint">仅填引用名；DSN/凭据不落库、不经前端（BASALT_TENANT_DB_* 环境变量注入）</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">创建</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="editDlg" title="编辑租户" width="480">
      <el-form label-width="90">
        <el-form-item label="名称"><el-input v-model="editForm.name" /></el-form-item>
        <el-form-item label="状态">
          <el-radio-group v-model="editForm.status">
            <el-radio value="active">启用</el-radio>
            <el-radio value="disabled">停用</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-divider content-position="left">隔离配置（创建时确定）</el-divider>
        <el-form-item label="隔离模式">
          <el-tag :type="isoTag(editing?.isolation_mode || '')" size="small">{{ isoLabel(editing?.isolation_mode || '') }}</el-tag>
        </el-form-item>
        <el-form-item label="库引用">
          <code v-if="editing?.db_ref" style="font-size: 12px">{{ editing.db_ref }}</code>
          <span v-else style="color: #909399">-</span>
        </el-form-item>
        <div class="hint">隔离模式与库引用在创建后不可变更（防止跨模式数据归属静默变化）；模式变更走新建租户 + 受控数据迁移。</div>
      </el-form>
      <template #footer>
        <el-button @click="editDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveEdit">保存</el-button>
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

// 字段语义依据 pkg/modules/tenant/tenant.go：isolation_mode 三枚举
// （shared-schema/shared-db/dedicated-db，默认 shared-schema）；
// db_ref 仅 dedicated-db 可且必填；两者创建时设置、PATCH 不可变更
// （updateTenantReq 仅 name/status，leader 裁决确认为设计边界）。
interface TenantRow {
  id: string
  code: string
  name: string
  isolation_mode: string
  db_ref?: string
  status: string
  created_at: string
}
interface UserRow { id: string; username: string; email: string; tenant_id: string; status: string }

const can = (p: string) => hasPerm(perms.get(), p)

const isoLabel = (m: string) => ({ 'shared-schema': '共享表', 'shared-db': '共享库', 'dedicated-db': '独立库' }[m] || m)
const isoTag = (m: string) => ({ 'shared-schema': 'primary', 'shared-db': 'success', 'dedicated-db': 'warning' }[m] || 'info')

const rows = ref<TenantRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const editDlg = ref(false)
const saving = ref(false)
const usersDlg = ref(false)
const viewing = ref<TenantRow | null>(null)
const editing = ref<TenantRow | null>(null)
const members = ref<UserRow[]>([])
const form = reactive({ code: '', name: '', isolation_mode: 'shared-schema', db_ref: '' })
const editForm = reactive({ name: '', status: 'active' })

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

// 切换隔离模式时清空 db_ref：与后端校验语义一致
// （db_ref only applies to dedicated-db，非 dedicated 传值会被 400 拒绝）
function onModeChange() {
  if (form.isolation_mode !== 'dedicated-db') form.db_ref = ''
}

function openCreate() {
  Object.assign(form, { code: '', name: '', isolation_mode: 'shared-schema', db_ref: '' })
  dlg.value = true
}

async function save() {
  if (!form.code || !form.name) {
    ElMessage.warning('代码与名称必填')
    return
  }
  // 前端预校验（与后端 tenant.go 校验语义一致）：
  // dedicated-db 必填 db_ref；非 dedicated 不传 db_ref
  if (form.isolation_mode === 'dedicated-db' && !form.db_ref.trim()) {
    ElMessage.warning('独立库模式必须填写库引用（db_ref）')
    return
  }
  saving.value = true
  try {
    await api('/api/v1/tenant/tenants', {
      method: 'POST',
      body: {
        code: form.code,
        name: form.name,
        isolation_mode: form.isolation_mode,
        ...(form.isolation_mode === 'dedicated-db' ? { db_ref: form.db_ref.trim() } : {}),
      },
    })
    ElMessage.success('已创建')
    dlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

function openEdit(row: TenantRow) {
  editing.value = row
  editForm.name = row.name
  editForm.status = row.status
  editDlg.value = true
}

async function saveEdit() {
  if (!editing.value) return
  saving.value = true
  try {
    // PATCH 仅 name/status 可变（后端语义）；isolation_mode/db_ref 不提交
    await api(`/api/v1/tenant/tenants/${editing.value.id}`, {
      method: 'PATCH',
      body: { name: editForm.name, status: editForm.status },
    })
    ElMessage.success('已保存')
    editDlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
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

<style scoped>
.hint { color: #909399; font-size: 12px; line-height: 1.6; margin-top: 4px; }
</style>

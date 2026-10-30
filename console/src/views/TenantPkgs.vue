<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('tenantpkg:manage')" type="primary" @click="openCreate">新建套餐</el-button>
      <el-button @click="load">刷新</el-button>
      <span class="hint">套餐=权限点模板（Perms 生效裁剪）+菜单模板（MenuIDs 展示用）；绑定后租户用户权限与菜单自动收窄</span>
    </div>
    <el-table :data="rows" border stripe size="small" v-loading="loading">
      <el-table-column prop="name" label="套餐名" width="160" />
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.status === 'active' ? 'success' : 'info'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="权限点" min-width="220">
        <template #default="{ row }">
          <el-tag v-for="p in (row.perms || []).slice(0, 6)" :key="p" size="small" style="margin: 2px">{{ p }}</el-tag>
          <el-tag v-if="(row.perms || []).length > 6" size="small" type="info">+{{ row.perms.length - 6 }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="菜单数" width="80">
        <template #default="{ row }">{{ (row.menu_ids || []).length || '-' }}</template>
      </el-table-column>
      <el-table-column prop="bound_tenants" label="绑定租户" width="90" />
      <el-table-column prop="remark" label="备注" min-width="140" show-overflow-tooltip />
      <el-table-column label="操作" width="220" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('tenantpkg:manage')" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button v-if="can('tenantpkg:manage')" size="small" type="primary" text @click="openBind(row)">绑定租户</el-button>
          <el-button v-if="can('tenantpkg:manage')" size="small" type="danger" @click="del(row)">删</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '编辑套餐' : '新建套餐'" width="600">
      <el-form label-width="90">
        <el-form-item label="套餐名" required><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="状态">
          <el-radio-group v-model="form.status">
            <el-radio value="active">启用</el-radio>
            <el-radio value="disabled">停用</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="权限点">
          <el-input v-model="form.permsText" type="textarea" :rows="4"
            placeholder="每行或逗号分隔；支持 * / 模块前缀通配（如 tenant:*），生效集为与角色权限的交集" />
        </el-form-item>
        <el-form-item label="菜单模板">
          <el-select v-model="form.menu_ids" multiple filterable style="width: 100%" placeholder="选择套餐包含的菜单（展示与裁剪参考）">
            <el-option v-for="m in menus" :key="m.id" :value="m.id" :label="m.name + '（' + (m.path || '目录') + '）'" />
          </el-select>
        </el-form-item>
        <el-form-item label="备注"><el-input v-model="form.remark" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="bindDlg" :title="`套餐绑定：${binding?.name || ''}`" width="520">
      <el-table :data="tenantRows" border size="small" max-height="360" v-loading="tenantsLoading">
        <el-table-column prop="code" label="代码" width="120" />
        <el-table-column prop="name" label="名称" width="150" />
        <el-table-column label="当前套餐" min-width="130">
          <template #default="{ row }">{{ pkgNameOf(row.pkgId) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button v-if="row.pkgId === binding?.id" size="small" type="warning" @click="bind(row, '')">解绑</el-button>
            <el-button v-else size="small" type="primary" text @click="bind(row, binding?.id || '')">绑定</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg, type MenuRow } from '../api'

// API 依据 pkg/modules/tenantpkg（platform-small-pack）
interface PkgRow {
  id: string
  name: string
  status: string
  menu_ids?: string[]
  perms?: string[]
  remark?: string
  bound_tenants?: number
}
interface TenantRow { id: string; code: string; name: string }

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<PkgRow[]>([])
const menus = ref<MenuRow[]>([])
const loading = ref(false)
const saving = ref(false)
const dlg = ref(false)
const bindDlg = ref(false)
const editing = ref<PkgRow | null>(null)
const binding = ref<PkgRow | null>(null)
const tenantRows = ref<Array<TenantRow & { pkgId: string }>>([])
const tenantsLoading = ref(false)
const form = reactive({ name: '', status: 'active', permsText: '', menu_ids: [] as string[], remark: '' })

async function load() {
  loading.value = true
  try {
    rows.value = await api<PkgRow[]>('/api/v1/tenantpkg/packages')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function loadMenus() {
  menus.value = await api<MenuRow[]>('/api/v1/menus').catch(() => [])
}

function resetForm() {
  Object.assign(form, { name: '', status: 'active', permsText: '', menu_ids: [], remark: '' })
}

function openCreate() {
  editing.value = null
  resetForm()
  dlg.value = true
}

function openEdit(row: PkgRow) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    status: row.status,
    permsText: (row.perms || []).join('\n'),
    menu_ids: [...(row.menu_ids || [])],
    remark: row.remark || '',
  })
  dlg.value = true
}

const parsePerms = () => form.permsText.split(/[\n,，;]+/).map((s) => s.trim()).filter(Boolean)

async function save() {
  if (!form.name) {
    ElMessage.warning('套餐名必填')
    return
  }
  saving.value = true
  try {
    const body = { name: form.name, status: form.status, perms: parsePerms(), menu_ids: form.menu_ids, remark: form.remark }
    if (editing.value) {
      await api(`/api/v1/tenantpkg/packages/${editing.value.id}`, { method: 'PATCH', body })
    } else {
      await api('/api/v1/tenantpkg/packages', { method: 'POST', body })
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

async function del(row: PkgRow) {
  try {
    await ElMessageBox.confirm(`删除套餐「${row.name}」？在用套餐（绑定 ${row.bound_tenants || 0} 租户）不可删除。`, '确认', { type: 'warning' })
    await api(`/api/v1/tenantpkg/packages/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    // 409 = 仍有租户在用
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

const pkgNameOf = (id: string) => rows.value.find((p) => p.id === id)?.name || '-'

async function openBind(row: PkgRow) {
  binding.value = row
  bindDlg.value = true
  tenantsLoading.value = true
  try {
    const ts = await api<TenantRow[]>('/api/v1/tenant/tenants')
    const withPkg: Array<TenantRow & { pkgId: string }> = []
    for (const t of ts) {
      const b = await api<{ package_id?: string } | null>(`/api/v1/tenantpkg/tenants/${t.id}/package`).catch(() => null)
      withPkg.push({ ...t, pkgId: b?.package_id || '' })
    }
    tenantRows.value = withPkg
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    tenantsLoading.value = false
  }
}

async function bind(t: TenantRow & { pkgId: string }, pkgId: string) {
  try {
    // PUT {package_id}：非空=绑定，空=解绑（后端语义）
    await api(`/api/v1/tenantpkg/tenants/${t.id}/package`, { method: 'PUT', body: { package_id: pkgId } })
    ElMessage.success(pkgId ? `已绑定「${pkgNameOf(pkgId)}」` : '已解绑')
    await openBind(binding.value!)
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

onMounted(() => { load(); loadMenus() })
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; }
</style>

<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('system:menu:manage')" type="primary" @click="openCreate()">新建菜单</el-button>
      <el-button @click="load">刷新</el-button>
      <span class="hint">后端统一下发；空下发时 Console 回退静态菜单（契约 §0）</span>
    </div>
    <el-table :data="tree" border row-key="id" default-expand-all v-loading="loading" size="small">
      <el-table-column prop="name" label="名称" width="200" />
      <el-table-column prop="path" label="路径" width="180" show-overflow-tooltip />
      <el-table-column prop="icon" label="图标" width="110" />
      <el-table-column prop="perm" label="权限点" min-width="170" show-overflow-tooltip>
        <template #default="{ row }">
          <code v-if="row.perm" style="font-size: 12px">{{ row.perm }}</code>
          <span v-else style="color: var(--semi-color-text-3)">目录</span>
        </template>
      </el-table-column>
      <el-table-column prop="sort" label="排序" width="70" />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '停用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="role_count" label="角色绑定" width="90" />
      <el-table-column label="操作" width="220" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('system:menu:manage')" size="small" @click="openCreate(row.id)">子项</el-button>
          <el-button v-if="can('system:menu:manage')" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button v-if="can('system:menu:manage')" size="small" type="primary" text @click="openBind(row)">绑定角色</el-button>
          <el-button v-if="can('system:menu:manage')" size="small" type="danger" @click="del(row)">删</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '编辑菜单' : '新建菜单'" width="520">
      <el-form label-width="90">
        <el-form-item label="父级">
          <el-tree-select v-model="form.parent_id" :data="parentOptions" check-strictly clearable
            node-key="id" :props="{ label: 'name', children: 'children' }" placeholder="留空为根菜单" style="width: 100%" />
        </el-form-item>
        <el-form-item label="名称" required><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="路径" required>
          <el-input v-model="form.path" placeholder="须为前端已注册路由，如 /users" />
        </el-form-item>
        <el-form-item label="图标"><el-input v-model="form.icon" placeholder="Element Plus 图标名，如 User（可空）" /></el-form-item>
        <el-form-item label="权限点">
          <el-input v-model="form.perm" placeholder="如 identity:user:list（目录节点留空）" />
        </el-form-item>
        <el-form-item label="排序"><el-input-number v-model="form.sort" :min="0" :max="9999" /></el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="bindDlg" title="绑定角色（以菜单为粒度）" width="480">
      <el-select v-model="pickedRoles" multiple filterable style="width: 100%" placeholder="选择持有本菜单的角色">
        <el-option v-for="r in roles" :key="r.id" :value="r.id" :label="`${r.code}（${r.name}）`" />
      </el-select>
      <div class="hint">绑定即把本菜单（含子树）加入角色的可见集合；以角色为粒度的整树配置请在角色管理中操作。</div>
      <template #footer>
        <el-button @click="bindDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveBind">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg, type MenuRow } from '../api'

interface RoleRow { id: string; code: string; name: string }
interface TreeRow extends MenuRow { children: TreeRow[] }

// 页面级 system:menu:list（路由 meta.perms）；写操作按钮级 system:menu:manage
const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<MenuRow[]>([])
const roles = ref<RoleRow[]>([])
const tree = computed<TreeRow[]>(() => buildTree(rows.value))
// 父级选择器选项：复用树（编辑时排除自身子树，防止成环——后端同样校验 409）
const parentOptions = computed<TreeRow[]>(() => {
  if (!editing.value) return tree.value
  const exclude = (nodes: TreeRow[]): TreeRow[] =>
    nodes
      .filter((n) => n.id !== editing.value?.id)
      .map((n) => ({ ...n, children: exclude(n.children) }))
  return exclude(tree.value)
})

const loading = ref(false)
const dlg = ref(false)
const bindDlg = ref(false)
const saving = ref(false)
const editing = ref<MenuRow | null>(null)
const binding = ref<MenuRow | null>(null)
const pickedRoles = ref<string[]>([])
const form = reactive({ parent_id: '', name: '', path: '', icon: '', perm: '', sort: 100, enabled: true })

function buildTree(list: MenuRow[]): TreeRow[] {
  const byId = new Map<string, TreeRow>()
  for (const r of list) byId.set(r.id, { ...r, children: [] })
  const roots: TreeRow[] = []
  for (const node of byId.values()) {
    if (node.parent_id && byId.has(node.parent_id)) byId.get(node.parent_id)!.children.push(node)
    else roots.push(node)
  }
  const sortRec = (nodes: TreeRow[]) => {
    nodes.sort((a, b) => a.sort - b.sort)
    nodes.forEach((n) => sortRec(n.children))
  }
  sortRec(roots)
  return roots
}

async function load() {
  loading.value = true
  try {
    // §2.2 GET /menus：扁平全量（含停用与 role_count）
    rows.value = await api<MenuRow[]>('/api/v1/menus')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function loadRoles() {
  roles.value = await api<RoleRow[]>('/api/v1/identity/roles').catch(() => [])
}

function resetForm() {
  Object.assign(form, { parent_id: '', name: '', path: '', icon: '', perm: '', sort: 100, enabled: true })
}

function openCreate(parentId?: string) {
  editing.value = null
  resetForm()
  if (parentId) form.parent_id = parentId
  dlg.value = true
}

function openEdit(row: MenuRow) {
  editing.value = row
  Object.assign(form, {
    parent_id: row.parent_id || '',
    name: row.name,
    path: row.path,
    icon: row.icon || '',
    perm: row.perm || '',
    sort: row.sort,
    enabled: row.enabled,
  })
  dlg.value = true
}

async function save() {
  if (!form.name || !form.path) {
    ElMessage.warning('名称与路径必填')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await api(`/api/v1/menus/${editing.value.id}`, { method: 'PATCH', body: { ...form } })
    } else {
      await api('/api/v1/menus', { method: 'POST', body: { ...form } })
    }
    ElMessage.success('已保存')
    dlg.value = false
    await load()
  } catch (e) {
    // 400 成环 / 409 重名 path 等按后端 message 提示
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function del(row: MenuRow) {
  try {
    await ElMessageBox.confirm(`删除菜单「${row.name}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/menus/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    // 409：有子节点 / 有角色绑定 → 后端 message 指引先解绑或先删子项
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

async function openBind(row: MenuRow) {
  binding.value = row
  // 以菜单为粒度反查当前已绑定角色（从角色侧读取逐个匹配成本高，
  // 后端提供按菜单聚合：遍历角色读 menu_ids 属契约内端点）
  try {
    const bound: string[] = []
    for (const r of roles.value) {
      const d = await api<{ menu_ids: string[] }>(`/api/v1/roles/${r.id}/menus`).catch(() => ({ menu_ids: [] as string[] }))
      if (d.menu_ids?.includes(row.id)) bound.push(r.id)
    }
    pickedRoles.value = bound
    bindDlg.value = true
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function saveBind() {
  if (!binding.value) return
  saving.value = true
  try {
    // 契约 §2.2：PUT /roles/{id}/menus 为全量替换，按选中差量逐角色更新
    for (const r of roles.value) {
      const cur = await api<{ menu_ids: string[] }>(`/api/v1/roles/${r.id}/menus`)
      const curSet = new Set(cur.menu_ids || [])
      const want = pickedRoles.value.includes(r.id)
      const has = curSet.has(binding.value.id)
      if (want !== has) {
        if (want) curSet.add(binding.value.id)
        else curSet.delete(binding.value.id)
        await api(`/api/v1/roles/${r.id}/menus`, { method: 'PUT', body: { menu_ids: [...curSet] } })
      }
    }
    ElMessage.success('绑定已更新')
    bindDlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

onMounted(() => { load(); loadRoles() })
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; line-height: 1.6; margin-top: 4px; }
</style>

<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('identity:role:manage')" type="primary" @click="dlg = true">新建角色</el-button>
      <el-button @click="load">刷新</el-button>
    </div>
    <el-table :data="rows" border stripe v-loading="loading">
      <el-table-column prop="code" label="代码" width="140" />
      <el-table-column prop="name" label="名称" width="160" />
      <el-table-column label="权限点">
        <template #default="{ row }">
          <el-tag v-for="p in row.permissions" :key="p" size="small" style="margin: 2px">{{ p }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="160" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('identity:role:manage')" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button v-if="can('identity:role:manage')" size="small" type="danger" @click="del(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '编辑角色' : '新建角色'" width="480">
      <el-form label-width="90">
        <el-form-item label="代码"><el-input v-model="form.code" :disabled="!!editing" placeholder="小写字母/数字/连字符" /></el-form-item>
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="权限点">
          <el-input v-model="form.perms" type="textarea" :rows="4"
            placeholder="逗号或换行分隔，支持 * 与前缀通配，如 identity:user:*" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

interface RoleRow { id: string; code: string; name: string; permissions?: string[] }

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<RoleRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const editing = ref<RoleRow | null>(null)
const form = reactive({ code: '', name: '', perms: '' })

async function load() {
  loading.value = true
  try {
    rows.value = await api<RoleRow[]>('/api/v1/identity/roles')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function openEdit(row: RoleRow) {
  editing.value = row
  form.code = row.code
  form.name = row.name
  form.perms = (row.permissions || []).join(', ')
  dlg.value = true
}

const parsePerms = () => form.perms.split(/[,，\n]/).map((s) => s.trim()).filter(Boolean)

async function save() {
  try {
    if (editing.value) {
      await api(`/api/v1/identity/roles/${editing.value.id}`, {
        method: 'PATCH',
        body: { name: form.name, permissions: parsePerms() },
      })
    } else {
      await api('/api/v1/identity/roles', {
        method: 'POST',
        body: { code: form.code, name: form.name, permissions: parsePerms() },
      })
    }
    ElMessage.success('已保存')
    dlg.value = false
    editing.value = null
    Object.assign(form, { code: '', name: '', perms: '' })
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function del(row: RoleRow) {
  try {
    await ElMessageBox.confirm(`删除角色 ${row.code}？`, '确认', { type: 'warning' })
    await api(`/api/v1/identity/roles/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('config:manage')" type="primary" @click="dlg = true">新增参数</el-button>
      <el-button @click="load">刷新</el-button>
    </div>
    <el-table :data="rows" border stripe v-loading="loading" size="small">
      <el-table-column prop="key" label="Key" width="220" show-overflow-tooltip />
      <el-table-column prop="name" label="名称" width="160" />
      <el-table-column prop="value" label="值" show-overflow-tooltip />
      <el-table-column prop="updated_at" label="更新时间" width="170">
        <template #default="{ row }">{{ new Date(row.updated_at).toLocaleString('zh-CN') }}</template>
      </el-table-column>
      <el-table-column label="操作" width="150" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('config:manage')" size="small" @click="openEdit(row)">改值</el-button>
          <el-button v-if="can('config:manage')" size="small" type="danger" @click="del(row)">删</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '修改参数' : '新增参数'" width="440">
      <el-form label-width="60">
        <el-form-item label="Key"><el-input v-model="form.key" :disabled="!!editing" placeholder="如 sys.title" /></el-form-item>
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="值"><el-input v-model="form.value" /></el-form-item>
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
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, perms, hasPerm, errMsg } from '../api'

interface ConfigRow { id: string; key: string; name: string; value: string; updated_at: string }

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<ConfigRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const editing = ref<ConfigRow | null>(null)
const form = reactive({ key: '', name: '', value: '' })

async function load() {
  loading.value = true
  try {
    rows.value = await api<ConfigRow[]>('/api/v1/configs')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function openEdit(row: ConfigRow) {
  editing.value = row
  Object.assign(form, { key: row.key, name: row.name, value: row.value })
  dlg.value = true
}

async function save() {
  if (!form.key) return
  try {
    if (editing.value) {
      await api(`/api/v1/configs/${editing.value.id}`, { method: 'PATCH', body: { value: form.value, name: form.name } })
    } else {
      await api('/api/v1/configs', { method: 'POST', body: { ...form } })
    }
    ElMessage.success('已保存')
    dlg.value = false
    editing.value = null
    Object.assign(form, { key: '', name: '', value: '' })
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function del(row: ConfigRow) {
  try {
    await ElMessageBox.confirm(`删除参数「${row.key}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/configs/${row.id}`, { method: 'DELETE' })
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

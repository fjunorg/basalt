<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('errcode:manage')" type="primary" @click="openCreate">新增错误码</el-button>
      <el-button @click="load">刷新</el-button>
      <el-select v-model="moduleFilter" clearable filterable placeholder="按模块过滤" size="small" style="width: 160px" @change="applyFilter">
        <el-option v-for="m in modules" :key="m" :value="m" :label="m" />
      </el-select>
      <span class="hint">错误码注册表：module+code 唯一；种子含 8 模块代表性抽样，持续补录</span>
    </div>
    <el-table :data="filtered" border stripe size="small" v-loading="loading">
      <el-table-column prop="module" label="模块" width="120" />
      <el-table-column prop="code" label="错误码" width="140" />
      <el-table-column prop="message" label="信息" min-width="180" show-overflow-tooltip />
      <el-table-column label="HTTP" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="httpTag(row.http_status)">{{ row.http_status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="detail" label="说明" min-width="200" show-overflow-tooltip />
      <el-table-column v-if="can('errcode:manage')" label="操作" width="150" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" type="danger" @click="del(row)">删</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '编辑错误码' : '新增错误码'" width="520">
      <el-form label-width="90">
        <el-form-item label="模块" required>
          <el-input v-model="form.module" placeholder="如 identity" :disabled="!!editing" />
        </el-form-item>
        <el-form-item label="错误码" required>
          <el-input v-model="form.code" :disabled="!!editing" placeholder="如 username_taken" />
        </el-form-item>
        <el-form-item label="信息" required><el-input v-model="form.message" /></el-form-item>
        <el-form-item label="HTTP 状态">
          <el-select v-model="form.http_status" style="width: 100%">
            <el-option v-for="s in [400, 401, 403, 404, 409, 422, 429, 500, 502, 503]" :key="s" :value="s" :label="String(s)" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明"><el-input v-model="form.detail" type="textarea" :rows="3" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

// API 依据 pkg/modules/errcode（platform-small-pack）：module+code 唯一键
interface ErrEntry {
  id: string
  module: string
  code: string
  message: string
  http_status: number
  detail?: string
}

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<ErrEntry[]>([])
const loading = ref(false)
const dlg = ref(false)
const saving = ref(false)
const editing = ref<ErrEntry | null>(null)
const moduleFilter = ref('')
const form = reactive({ module: '', code: '', message: '', http_status: 400, detail: '' })

const modules = computed(() => [...new Set(rows.value.map((r) => r.module))].sort())
const filtered = computed(() => moduleFilter.value ? rows.value.filter((r) => r.module === moduleFilter.value) : rows.value)

const httpTag = (s: number) => (s >= 500 ? 'danger' : s >= 400 ? 'warning' : 'success')

async function load() {
  loading.value = true
  try {
    rows.value = await api<ErrEntry[]>('/api/v1/errcodes')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function applyFilter() { /* filtered 计算属性即时生效 */ }

function openCreate() {
  editing.value = null
  Object.assign(form, { module: '', code: '', message: '', http_status: 400, detail: '' })
  dlg.value = true
}

function openEdit(row: ErrEntry) {
  editing.value = row
  Object.assign(form, { module: row.module, code: row.code, message: row.message, http_status: row.http_status, detail: row.detail || '' })
  dlg.value = true
}

async function save() {
  if (!form.module || !form.code || !form.message) {
    ElMessage.warning('模块/错误码/信息必填')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      // PATCH：module/code 为唯一键不可变（编辑态禁用输入）
      await api(`/api/v1/errcodes/${editing.value.id}`, {
        method: 'PATCH',
        body: { message: form.message, http_status: form.http_status, detail: form.detail },
      })
    } else {
      await api('/api/v1/errcodes', { method: 'POST', body: { ...form } })
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

async function del(row: ErrEntry) {
  try {
    await ElMessageBox.confirm(`删除错误码 ${row.module}/${row.code}？`, '确认', { type: 'warning' })
    await api(`/api/v1/errcodes/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; }
</style>

<template>
  <div class="page">
    <el-row :gutter="16">
      <el-col :span="8">
        <el-card>
          <template #header>
            字典类型
            <el-button v-if="can('dict:type:manage')" size="small" type="primary" style="float: right" @click="typeDlg = true">新建</el-button>
          </template>
          <el-table :data="types" border highlight-current-row size="small" @current-change="pickType">
            <el-table-column prop="code" label="代码" width="110" />
            <el-table-column prop="name" label="名称" />
            <el-table-column label="操作" width="70">
              <template #default="{ row }">
                <el-button size="small" text type="danger" @click="delType(row)">删</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :span="16">
        <el-card>
          <template #header>
            字典数据{{ current ? `（${current.code}）` : '' }}
            <el-button v-if="current && can('dict:data:manage')" size="small" type="primary" style="float: right" @click="dataDlg = true">新增</el-button>
          </template>
          <el-empty v-if="!current" description="选择左侧字典类型" :image-size="60" />
          <el-table v-else :data="datas" border size="small">
            <el-table-column prop="label" label="标签" width="140" />
            <el-table-column prop="value" label="值" width="140" />
            <el-table-column prop="sort" label="排序" width="70" />
            <el-table-column prop="status" label="状态" width="80" />
            <el-table-column label="操作" width="70" fixed="right">
              <template #default="{ row }">
                <el-button size="small" text type="danger" @click="delData(row)">删</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>

    <el-dialog v-model="typeDlg" title="新建字典类型" width="400">
      <el-form label-width="60">
        <el-form-item label="代码"><el-input v-model="typeForm.code" placeholder="如 gender" /></el-form-item>
        <el-form-item label="名称"><el-input v-model="typeForm.name" /></el-form-item>
        <el-form-item label="备注"><el-input v-model="typeForm.remark" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="typeDlg = false">取消</el-button>
        <el-button type="primary" @click="saveType">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="dataDlg" :title="`新增数据（${current?.code || ''}）`" width="400">
      <el-form label-width="60">
        <el-form-item label="标签"><el-input v-model="dataForm.label" /></el-form-item>
        <el-form-item label="值"><el-input v-model="dataForm.value" /></el-form-item>
        <el-form-item label="排序"><el-input-number v-model="dataForm.sort" :min="0" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dataDlg = false">取消</el-button>
        <el-button type="primary" @click="saveData">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, hasPerm } from '../api'

const perms = JSON.parse(localStorage.getItem('basalt_perms') || '[]')
const can = (p) => hasPerm(perms, p)

const types = ref([])
const datas = ref([])
const current = ref(null)
const typeDlg = ref(false)
const dataDlg = ref(false)
const typeForm = reactive({ code: '', name: '', remark: '' })
const dataForm = reactive({ label: '', value: '', sort: 0 })

async function loadTypes() {
  types.value = await api('/api/v1/dict/types').catch(() => [])
}

async function pickType(row) {
  current.value = row
  if (row) datas.value = await api(`/api/v1/dict/datas?type=${row.code}`).catch(() => [])
}

async function saveType() {
  if (!typeForm.code || !typeForm.name) return
  try {
    await api('/api/v1/dict/types', { method: 'POST', body: { ...typeForm } })
    ElMessage.success('已保存')
    typeDlg.value = false
    Object.assign(typeForm, { code: '', name: '', remark: '' })
    await loadTypes()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function delType(row) {
  try {
    await ElMessageBox.confirm(`删除类型「${row.code}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/dict/types/${row.id}`, { method: 'DELETE' })
    if (current.value?.id === row.id) { current.value = null; datas.value = [] }
    await loadTypes()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(e.message)
  }
}

async function saveData() {
  if (!dataForm.label || !dataForm.value) return
  try {
    await api('/api/v1/dict/datas', {
      method: 'POST',
      body: { ...dataForm, type_code: current.value.code },
    })
    ElMessage.success('已保存')
    dataDlg.value = false
    Object.assign(dataForm, { label: '', value: '', sort: 0 })
    await pickType(current.value)
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function delData(row) {
  try {
    await ElMessageBox.confirm(`删除「${row.label}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/dict/datas/${row.id}`, { method: 'DELETE' })
    await pickType(current.value)
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(e.message)
  }
}

onMounted(loadTypes)
</script>

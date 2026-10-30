<template>
  <div class="page">
    <div class="toolbar">
      <el-button type="primary" @click="dlg = true">登记合同</el-button>
      <el-button @click="load">刷新</el-button>
      <el-radio-group v-model="scope" size="small" @change="load">
        <el-radio-button value="mine">我的</el-radio-button>
        <el-radio-button value="all">全量</el-radio-button>
      </el-radio-group>
    </div>
    <el-table :data="rows" border stripe size="small" v-loading="loading">
      <el-table-column prop="contract_num" label="合同号" width="150" show-overflow-tooltip />
      <el-table-column prop="title" label="名称" min-width="180" show-overflow-tooltip />
      <el-table-column prop="party" label="对方单位" min-width="150" show-overflow-tooltip />
      <el-table-column label="金额" width="120">
        <template #default="{ row }">¥{{ (row.amount / 100).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="到期" width="110">
        <template #default="{ row }">
          <span v-if="row.expire_date" :style="expiring(row) ? 'color:#f56c6c;font-weight:600' : ''">
            {{ new Date(row.expire_date).toLocaleDateString('zh-CN') }}
          </span>
          <span v-else>-</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="stType(row.status)">{{ stLabel(row.status) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="170" fixed="right">
        <template #default="{ row }">
          <el-button v-if="row.status === 'draft' || row.status === 'rejected'" size="small" type="success"
            @click="submit(row)">提交会签</el-button>
          <el-button v-if="row.status === 'active' && can('contract:manage')" size="small" type="warning"
            @click="finish(row)">完结</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" title="登记合同" width="520">
      <el-form label-width="80">
        <el-form-item label="合同号"><el-input v-model="form.contract_num" placeholder="如 HT-2026-001" /></el-form-item>
        <el-form-item label="名称"><el-input v-model="form.title" /></el-form-item>
        <el-form-item label="对方单位"><el-input v-model="form.party" /></el-form-item>
        <el-form-item label="金额(分)"><el-input-number v-model="form.amount" :min="1" :step="100000" style="width: 100%" /></el-form-item>
        <el-form-item label="签订日期"><el-input v-model="form.sign_date" placeholder="YYYY-MM-DD" /></el-form-item>
        <el-form-item label="到期日期"><el-input v-model="form.expire_date" placeholder="YYYY-MM-DD" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api, hasPerm } from '../api'

const perms = JSON.parse(localStorage.getItem('basalt_perms') || '[]')
const can = (p) => hasPerm(perms, p)

const rows = ref([])
const loading = ref(false)
const dlg = ref(false)
const scope = ref('mine')
const form = reactive({ contract_num: '', title: '', party: '', amount: 1000000, sign_date: '', expire_date: '' })

const stLabel = (s) => ({ draft: '草稿', reviewing: '会签中', active: '生效中', rejected: '被驳回', finished: '已完结' }[s] || s)
const stType = (s) => ({ draft: 'info', reviewing: 'warning', active: 'success', rejected: 'danger', finished: '' }[s] || 'info')

const expiring = (row) => {
  if (!row.expire_date || row.status !== 'active') return false
  const days = (new Date(row.expire_date) - Date.now()) / 86400000
  return days >= 0 && days <= 30
}

async function load() {
  loading.value = true
  try {
    const q = scope.value === 'all' ? '?scope=all' : ''
    rows.value = await api('/api/v1/contracts' + q)
  } catch (e) {
    rows.value = []
    ElMessage.warning(e.message)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!form.title || !form.party || form.amount <= 0) {
    ElMessage.warning('名称、对方单位与金额必填')
    return
  }
  try {
    await api('/api/v1/contracts', { method: 'POST', body: { ...form } })
    ElMessage.success('已登记')
    dlg.value = false
    Object.assign(form, { contract_num: '', title: '', party: '', amount: 1000000, sign_date: '', expire_date: '' })
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function submit(row) {
  try {
    await api(`/api/v1/contracts/${row.id}/submit`, { method: 'POST' })
    ElMessage.success('已提交会签')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function finish(row) {
  try {
    await api(`/api/v1/contracts/${row.id}/finish`, { method: 'POST' })
    ElMessage.success('已完结')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="toolbar">
      <el-button type="primary" @click="dlg = true">拟稿 / 登记</el-button>
      <el-button @click="load">刷新</el-button>
      <el-radio-group v-model="scope" size="small" @change="load">
        <el-radio-button value="mine">我的</el-radio-button>
        <el-radio-button value="all">全量</el-radio-button>
      </el-radio-group>
    </div>
    <el-table :data="rows" border stripe size="small" v-loading="loading">
      <el-table-column prop="doc_num" label="文号" width="180" show-overflow-tooltip />
      <el-table-column label="类型" width="70">
        <template #default="{ row }">
          <el-tag size="small" :type="row.type === 'outbound' ? 'primary' : 'success'">
            {{ row.type === 'outbound' ? '发文' : '收文' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="title" label="标题" min-width="200" show-overflow-tooltip />
      <el-table-column label="密级" width="70">
        <template #default="{ row }">
          <el-tag size="small" :type="row.security === '普通' ? 'info' : 'danger'">{{ row.security }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="urgency" label="缓急" width="70" />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="stType(row.status)">{{ stLabel(row.status) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="170" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text @click="view(row)">正文</el-button>
          <el-button v-if="(row.status === 'draft' || row.status === 'rejected')" size="small" type="success"
            @click="submit(row)">提交</el-button>
          <el-button v-if="row.status === 'approved' && can('document:manage')" size="small" type="warning"
            @click="archive(row)">归档</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" title="公文拟稿 / 收文登记" width="560">
      <el-form label-width="80">
        <el-form-item label="类型">
          <el-radio-group v-model="form.type">
            <el-radio value="outbound">发文</el-radio>
            <el-radio value="inbound">收文</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="文号"><el-input v-model="form.doc_num" placeholder="如 玄武〔2026〕12号" /></el-form-item>
        <el-form-item label="标题"><el-input v-model="form.title" /></el-form-item>
        <el-form-item label="密级">
          <el-select v-model="form.security" style="width: 100%">
            <el-option label="普通" value="普通" />
            <el-option label="秘密" value="秘密" />
            <el-option label="机密" value="机密" />
          </el-select>
        </el-form-item>
        <el-form-item label="缓急">
          <el-select v-model="form.urgency" style="width: 100%">
            <el-option label="平急" value="平急" />
            <el-option label="加急" value="加急" />
            <el-option label="特急" value="特急" />
          </el-select>
        </el-form-item>
        <el-form-item label="对向单位"><el-input v-model="form.correspond" placeholder="主送/来文单位" /></el-form-item>
        <el-form-item label="正文"><el-input v-model="form.content" type="textarea" :rows="5" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="viewDlg" :title="viewing?.title" width="600">
      <p style="color: #909399; font-size: 13px">
        {{ viewing?.doc_num }} · {{ viewing?.type === 'outbound' ? '发文' : '收文' }} · {{ viewing?.security }} · {{ viewing?.urgency }}
      </p>
      <div style="white-space: pre-wrap; line-height: 1.8">{{ viewing?.content }}</div>
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
const viewDlg = ref(false)
const viewing = ref(null)
const scope = ref('mine')
const form = reactive({ type: 'outbound', doc_num: '', title: '', security: '普通', urgency: '平急', correspond: '', content: '' })

const stLabel = (s) => ({ draft: '草稿', circulating: '流转中', approved: '待归档', rejected: '已退回', archived: '已归档' }[s] || s)
const stType = (s) => ({ draft: 'info', circulating: 'warning', approved: 'success', rejected: 'danger', archived: '' }[s] || 'info')

async function load() {
  loading.value = true
  try {
    const q = scope.value === 'all' ? '?scope=all' : ''
    rows.value = await api('/api/v1/documents' + q)
  } catch (e) {
    rows.value = []
    ElMessage.warning(e.message)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!form.title) return
  try {
    await api('/api/v1/documents', { method: 'POST', body: { ...form } })
    ElMessage.success('已保存')
    dlg.value = false
    Object.assign(form, { type: 'outbound', doc_num: '', title: '', security: '普通', urgency: '平急', correspond: '', content: '' })
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function submit(row) {
  try {
    await api(`/api/v1/documents/${row.id}/submit`, { method: 'POST' })
    ElMessage.success('已提交审签')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function archive(row) {
  try {
    await api(`/api/v1/documents/${row.id}/archive`, { method: 'POST' })
    ElMessage.success('已归档')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

function view(row) {
  viewing.value = row
  viewDlg.value = true
}

onMounted(load)
</script>

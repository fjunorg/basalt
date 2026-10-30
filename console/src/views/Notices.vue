<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('notice:manage')" type="primary" @click="dlg = true">新建公告</el-button>
      <el-button @click="load">刷新</el-button>
      <el-radio-group v-model="scope" size="small" @change="load">
        <el-radio-button value="all">全部</el-radio-button>
        <el-radio-button value="published">已发布</el-radio-button>
      </el-radio-group>
    </div>
    <el-table :data="rows" border stripe v-loading="loading" size="small">
      <el-table-column prop="title" label="标题" min-width="200" show-overflow-tooltip />
      <el-table-column prop="type" label="类型" width="110" />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.status === 'published' ? 'success' : row.status === 'draft' ? 'warning' : 'info'" size="small">
            {{ row.status }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="created_at" label="创建时间" width="170">
        <template #default="{ row }">{{ new Date(row.created_at).toLocaleString('zh-CN') }}</template>
      </el-table-column>
      <el-table-column label="操作" width="220" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text @click="view(row)">查看</el-button>
          <el-button v-if="row.status === 'draft' && can('notice:publish')" size="small" type="success" @click="publish(row)">发布</el-button>
          <el-button v-if="row.status === 'published' && can('notice:publish')" size="small" type="warning" @click="revoke(row)">撤回</el-button>
          <el-button v-if="can('notice:manage')" size="small" type="danger" @click="del(row)">删</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" title="新建公告" width="520">
      <el-form label-width="60">
        <el-form-item label="标题"><el-input v-model="form.title" /></el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type" style="width: 100%">
            <el-option label="通知" value="notice" />
            <el-option label="公告" value="announcement" />
          </el-select>
        </el-form-item>
        <el-form-item label="内容"><el-input v-model="form.content" type="textarea" :rows="5" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" @click="save">保存草稿</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="viewDlg" :title="viewing?.title" width="560">
      <p style="color: var(--semi-color-text-2); font-size: 13px">
        {{ viewing?.type }} · {{ viewing?.status }} · {{ new Date(viewing?.created_at || 0).toLocaleString('zh-CN') }}
      </p>
      <div style="white-space: pre-wrap; line-height: 1.8">{{ viewing?.content }}</div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

interface NoticeRow { id: string; title: string; content: string; type: string; status: string; created_at: string }

const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<NoticeRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const viewDlg = ref(false)
const viewing = ref<NoticeRow | null>(null)
const scope = ref('all')
const form = reactive({ title: '', content: '', type: 'notice' })

async function load() {
  loading.value = true
  try {
    rows.value = await api<NoticeRow[]>(scope.value === 'published' ? '/api/v1/notices/published' : '/api/v1/notices')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function view(row: NoticeRow) {
  viewing.value = row
  viewDlg.value = true
}

async function save() {
  if (!form.title) return
  try {
    await api('/api/v1/notices', { method: 'POST', body: { ...form } })
    ElMessage.success('草稿已保存')
    dlg.value = false
    Object.assign(form, { title: '', content: '', type: 'notice' })
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function publish(row: NoticeRow) {
  try {
    await api(`/api/v1/notices/${row.id}/publish`, { method: 'POST' })
    ElMessage.success('已发布')
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function revoke(row: NoticeRow) {
  try {
    await ElMessageBox.confirm(`撤回「${row.title}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/notices/${row.id}/revoke`, { method: 'POST' })
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

async function del(row: NoticeRow) {
  try {
    await ElMessageBox.confirm(`删除「${row.title}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/notices/${row.id}`, { method: 'DELETE' })
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

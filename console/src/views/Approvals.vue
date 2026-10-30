<template>
  <div class="page">
    <el-tabs v-model="tab">
      <el-tab-pane label="我的待办" name="todo">
        <div class="toolbar">
          <el-button @click="loadTodo">刷新</el-button>
        </div>
        <el-table :data="todo" border stripe v-loading="loading">
          <el-table-column prop="business_key" label="业务号" width="160" />
          <el-table-column prop="summary" label="摘要" />
          <el-table-column prop="status" label="状态" width="90">
            <template #default="{ row }">
              <el-tag size="small">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="created_at" label="发起时间" width="180" :formatter="fmtTime" />
          <el-table-column label="操作" width="160" fixed="right">
            <template #default="{ row }">
              <el-button size="small" type="success" @click="act(row, 'approve')">通过</el-button>
              <el-button size="small" type="danger" @click="act(row, 'reject')">驳回</el-button>
              <el-button size="small" @click="openDelegate(row)">转办</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="实例列表" name="list">
        <div class="toolbar">
          <el-radio-group v-model="listScope" size="small" @change="loadInstances">
            <el-radio-button value="mine">我发起的</el-radio-button>
            <el-radio-button value="all">全部</el-radio-button>
          </el-radio-group>
          <el-button size="small" @click="loadInstances">刷新</el-button>
        </div>
        <el-table :data="instances" border stripe size="small" v-loading="listLoading">
          <el-table-column prop="business_key" label="业务号" width="150" />
          <el-table-column prop="summary" label="摘要" />
          <el-table-column prop="status" label="状态" width="100">
            <template #default="{ row }">
              <el-tag size="small" :type="statusType(row.status)">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="mode" label="模式" width="100" />
          <el-table-column prop="created_at" label="发起时间" width="180">
            <template #default="{ row }">{{ new Date(row.created_at).toLocaleString('zh-CN') }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text type="primary" @click="viewDetail(row)">详情</el-button>
            </template>
          </el-table-column>
        </el-table>
        <el-dialog v-model="detailDlg" title="实例详情" width="560">
          <p><b>业务号：</b>{{ detail?.instance?.business_key }}　<b>状态：</b>{{ detail?.instance?.status }}</p>
          <p><b>模式：</b>{{ detail?.instance?.mode === 'countersign' ? '会签' : '或签' }}</p>
          <el-timeline style="margin-top: 12px">
            <el-timeline-item v-for="(a, i) in detail?.actions || []" :key="i"
              :timestamp="new Date(a.time).toLocaleString('zh-CN')"
              :type="a.act === 'approve' ? 'success' : a.act === 'reject' ? 'danger' : 'info'">
              {{ a.act }} · {{ a.actor_id?.slice(0, 8) }} {{ a.comment ? '：' + a.comment : '' }}
            </el-timeline-item>
          </el-timeline>
        </el-dialog>
      </el-tab-pane>

      <el-tab-pane label="发起审批" name="create">
        <el-form label-width="90" style="max-width: 560px; padding-top: 12px">
          <el-form-item label="流程定义">
            <el-select v-model="form.defKey" clearable placeholder="选择预定义流程（空=手动指定）" style="width: 100%" @change="onDefPick">
              <el-option v-for="d in defs" :key="d.key" :value="d.key"
                :label="d.name + '（' + d.steps.length + ' 步）'" :disabled="d.status !== 'active'" />
            </el-select>
          </el-form-item>
          <el-form-item v-if="form.defKey" label="步骤预览">
            <el-tag v-for="(st, i) in pickedSteps" :key="i" style="margin: 2px" size="small">
              {{ i + 1 }}. {{ st.name }}（{{ st.mode === 'or-sign' ? '或签' : '会签' }}）
            </el-tag>
          </el-form-item>
          <el-form-item label="业务号"><el-input v-model="form.business_key" placeholder="如 EXP-2026-001" /></el-form-item>
          <el-form-item label="摘要"><el-input v-model="form.summary" /></el-form-item>
          <template v-if="!form.defKey">
            <el-form-item label="审批人">
              <el-select v-model="form.approvers" multiple style="width: 100%" placeholder="选择审批人（多选=会签）">
                <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
              </el-select>
            </el-form-item>
            <el-form-item label="模式">
              <el-radio-group v-model="form.mode">
                <el-radio value="countersign">会签（全员通过）</el-radio>
                <el-radio value="or-sign">或签（首动作定局）</el-radio>
              </el-radio-group>
            </el-form-item>
          </template>
          <el-form-item>
            <el-button type="primary" :loading="saving" @click="submit">发起</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="dlgDelegate" title="转办" width="400">
      <el-select v-model="delegateTo" filterable placeholder="转交给" style="width: 100%">
        <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
      </el-select>
      <template #footer>
        <el-button @click="dlgDelegate = false">取消</el-button>
        <el-button type="primary" @click="doDelegate">确认转办</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'

const statusType = (s) => ({ approved: 'success', rejected: 'danger', canceled: 'info' }[s] || 'warning')

const tab = ref('todo')
const todo = ref([])
const users = ref([])
const loading = ref(false)
const saving = ref(false)
const form = reactive({ business_key: '', summary: '', approvers: [], mode: 'countersign', defKey: '' })
const defs = ref([])
const pickedSteps = ref([])
const dlgDelegate = ref(false)
const delegateTo = ref('')
const delegating = ref(null)

async function loadDefs() {
  defs.value = await api('/api/v1/wf/definitions').catch(() => [])
}

function onDefPick(key) {
  pickedSteps.value = defs.value.find((d) => d.key === key)?.steps || []
}

function openDelegate(row) {
  delegating.value = row
  delegateTo.value = ''
  dlgDelegate.value = true
}

async function doDelegate() {
  if (!delegateTo.value) return
  try {
    await api(`/api/v1/wf/instances/${delegating.value.id}/delegate`, {
      method: 'POST',
      body: { to_user_id: delegateTo.value },
    })
    ElMessage.success('已转办')
    dlgDelegate.value = false
    await loadTodo()
  } catch (e) {
    ElMessage.error(e.message)
  }
}
const listScope = ref('mine')
const instances = ref([])
const listLoading = ref(false)
const detailDlg = ref(false)
const detail = ref(null)

async function loadInstances() {
  listLoading.value = true
  try {
    const path = listScope.value === 'all' ? '/api/v1/wf/instances/all' : '/api/v1/wf/instances/mine'
    const d = await api(path)
    instances.value = d.items || []
  } catch (e) {
    instances.value = []
    ElMessage.warning('无权查看全部实例（' + e.message + '）')
  } finally {
    listLoading.value = false
  }
}

async function viewDetail(row) {
  detail.value = await api('/api/v1/wf/instances/' + row.id).catch(() => null)
  if (!detail.value) { ElMessage.error('无权查看详情'); return }
  detailDlg.value = true
}

const fmtTime = (r, c, v) => new Date(v).toLocaleString('zh-CN')

async function loadTodo() {
  loading.value = true
  try {
    const [t, u] = await Promise.all([
      api('/api/v1/wf/todo'),
      api('/api/v1/identity/users').catch(() => []),
    ])
    todo.value = t.items || []
    users.value = u
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

async function submit() {
  if (!form.business_key || !form.summary) {
    ElMessage.warning('请填写业务号与摘要')
    return
  }
  if (!form.defKey && !form.approvers.length) {
    ElMessage.warning('选择流程定义或手动指定审批人')
    return
  }
  saving.value = true
  try {
    if (form.defKey) {
      await api(`/api/v1/wf/instances/by-definition/${form.defKey}`, {
        method: 'POST',
        body: { business_key: form.business_key, summary: form.summary },
      })
    } else {
      await api('/api/v1/wf/instances', { method: 'POST', body: { ...form } })
    }
    ElMessage.success('已发起')
    Object.assign(form, { business_key: '', summary: '', approvers: [], mode: 'countersign', defKey: '' })
    pickedSteps.value = []
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}

async function act(row, kind) {
  try {
    await api(`/api/v1/wf/instances/${row.id}/${kind}`, { method: 'POST', body: { comment: kind === 'approve' ? '同意' : '驳回' } })
    ElMessage.success(kind === 'approve' ? '已通过' : '已驳回')
    await loadTodo()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

onMounted(() => { loadTodo(); loadInstances(); loadDefs() })
</script>

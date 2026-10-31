<template>
  <div class="page">
    <el-tabs v-model="tab">
      <!-- 我的待办 -->
      <el-tab-pane label="我的待办" name="todo">
        <div class="toolbar">
          <el-button @click="loadTodo">刷新</el-button>
        </div>
        <el-table :data="todo" border stripe v-loading="loading">
          <el-table-column prop="business_key" label="业务号" width="150" />
          <el-table-column prop="summary" label="摘要" />
          <el-table-column prop="status" label="状态" width="85">
            <template #default="{ row }">
              <el-tag size="small">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="轮次" width="70">
            <template #default="{ row }">第 {{ (row.round || 0) + 1 }} 轮</template>
          </el-table-column>
          <el-table-column prop="created_at" label="发起时间" width="165" :formatter="fmtTime" />
          <el-table-column label="操作" width="250" fixed="right">
            <template #default="{ row }">
              <el-button size="small" type="success" @click="act(row, 'approve')">通过</el-button>
              <el-button size="small" type="danger" @click="act(row, 'reject')">驳回</el-button>
              <el-dropdown trigger="click" @command="(cmd: string) => moreAction(cmd, row)">
                <el-button size="small">更多<el-icon><ArrowDown /></el-icon></el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="cc">抄送他人</el-dropdown-item>
                    <el-dropdown-item command="transfer">转办（永久转移）</el-dropdown-item>
                    <el-dropdown-item command="delegate-act">委派（本步代办）</el-dropdown-item>
                    <el-dropdown-item command="addsign">加签（前/后）</el-dropdown-item>
                    <el-dropdown-item command="reject-to">驳回至指定节点</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 实例列表 -->
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
          <el-table-column prop="status" label="状态" width="95">
            <template #default="{ row }">
              <el-tag size="small" :type="statusType(row.status)">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="轮次" width="70">
            <template #default="{ row }">第 {{ (row.round || 0) + 1 }} 轮</template>
          </el-table-column>
          <el-table-column prop="created_at" label="发起时间" width="165">
            <template #default="{ row }">{{ new Date(row.created_at).toLocaleString('zh-CN') }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text type="primary" @click="viewDetail(row)">详情</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 抄送我的 -->
      <el-tab-pane label="抄送我的" name="cc">
        <div class="toolbar">
          <el-button size="small" @click="loadCc">刷新</el-button>
          <span class="hint">被抄送的实例快照（只读知会，不产生审批负担）</span>
        </div>
        <el-table :data="ccList" border stripe size="small" v-loading="ccLoading">
          <el-table-column prop="business_key" label="业务号" width="160" />
          <el-table-column prop="summary" label="摘要" />
          <el-table-column prop="status" label="状态" width="95">
            <template #default="{ row }">
              <el-tag size="small" :type="statusType(row.status)">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="created_at" label="发起时间" width="165">
            <template #default="{ row }">{{ new Date(row.created_at).toLocaleString('zh-CN') }}</template>
          </el-table-column>
        </el-table>
        <el-empty v-if="!ccList.length" description="暂无被抄送的流程" :image-size="60" />
      </el-tab-pane>

      <!-- 发起审批 -->
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
              {{ i + 1 }}. {{ st.name }}（{{ modeLabel(st.mode) }}）
            </el-tag>
          </el-form-item>
          <el-form-item label="业务号"><el-input v-model="form.business_key" placeholder="如 EXP-2026-001" /></el-form-item>
          <el-form-item label="摘要"><el-input v-model="form.summary" /></el-form-item>
          <template v-if="!form.defKey">
            <el-form-item label="审批人">
              <el-select v-model="form.approvers" multiple style="width: 100%" placeholder="选择审批人（多选；依次模式按选择顺序）">
                <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
              </el-select>
            </el-form-item>
            <el-form-item label="模式">
              <el-radio-group v-model="form.mode">
                <el-radio value="countersign">会签（全员通过）</el-radio>
                <el-radio value="or-sign">或签（首动作定局）</el-radio>
                <el-radio value="sequential">依次（按序逐人）</el-radio>
              </el-radio-group>
              <div class="hint" v-if="form.mode === 'sequential'">审批人按选择顺序逐人审批，未轮到者操作无效</div>
            </el-form-item>
          </template>
          <el-form-item>
            <el-button type="primary" :loading="saving" @click="submit">发起</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>
    </el-tabs>

    <!-- 实例详情 -->
    <el-dialog v-model="detailDlg" title="实例详情" width="640">
      <p>
        <b>业务号：</b>{{ detail?.instance?.business_key }}　<b>状态：</b>{{ detail?.instance?.status }}
        <el-tag v-if="(detail?.instance?.round || 0) > 0" size="small" type="warning" style="margin-left: 8px">第 {{ (detail?.instance?.round || 0) + 1 }} 轮</el-tag>
      </p>
      <p><b>模式：</b>{{ modeLabel(detail?.instance?.mode) }}</p>
      <p v-if="detail?.instance?.cc_user_ids?.length">
        <b>抄送：</b>
        <el-tag v-for="uid in detail.instance.cc_user_ids" :key="uid" size="small" style="margin: 2px">{{ userNameOf(uid) }}</el-tag>
      </p>
      <p v-if="detail?.instance?.delegations?.length">
        <b>委托：</b>
        <span v-for="(d, i) in detail.instance.delegations" :key="i" class="hint" style="margin-right: 10px">
          第 {{ (d.step_index ?? 0) + 1 }} 步 {{ userNameOf(d.from) }} → {{ userNameOf(d.to) }}（{{ d.active ? '生效中' : '已消耗' }}）
        </span>
      </p>
      <template v-if="detailSteps.length">
        <el-divider content-position="left">步骤</el-divider>
        <el-tag v-for="(st, i) in detailSteps" :key="i" size="small" style="margin: 3px"
          :type="i === currentStepIndex ? 'primary' : 'info'">
          {{ st.mode === 'sequential' ? (i + 1) + '. ' : '' }}{{ st.name }}（{{ modeLabel(st.mode) }}）
        </el-tag>
      </template>
      <el-divider content-position="left">动作历史</el-divider>
      <el-timeline>
        <el-timeline-item v-for="(a, i) in detail?.actions || []" :key="i"
          :timestamp="new Date(a.time).toLocaleString('zh-CN')"
          :type="actTag(a.act)">
          <b>{{ actLabel(a.act) }}</b> · {{ userNameOf(a.actor_id) }}
          <span v-if="a.delegate_from" class="hint">（受 {{ userNameOf(a.delegate_from) }} 委托代审）</span>
          <span v-if="(a.round || 0) > 0" class="hint">· 第 {{ (a.round || 0) + 1 }} 轮</span>
          <div v-if="a.comment" class="hint">{{ a.comment }}</div>
        </el-timeline-item>
      </el-timeline>
    </el-dialog>

    <!-- 抄送 -->
    <el-dialog v-model="ccDlg" title="抄送他人" width="440">
      <el-select v-model="ccTargets" multiple filterable style="width: 100%" placeholder="选择被抄送人（可多选，重复抄送幂等）">
        <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
      </el-select>
      <div class="hint">被抄送人可在「抄送我的」查看实例（只读知会）；不影响审批流程。</div>
      <template #footer>
        <el-button @click="ccDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doCc">抄送</el-button>
      </template>
    </el-dialog>

    <!-- 转办（永久转移） -->
    <el-dialog v-model="transferDlg" title="转办" width="440">
      <el-select v-model="transferTo" filterable style="width: 100%" placeholder="转交给（永久转移审批权）">
        <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
      </el-select>
      <div class="hint">转办后你的审批权整体转移给对方（本步审批人名单替换），历史保留。</div>
      <template #footer>
        <el-button @click="transferDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doTransfer">确认转办</el-button>
      </template>
    </el-dialog>

    <!-- 委派（本步一次性代办） -->
    <el-dialog v-model="delegateDlg" title="委派（一次性代办）" width="440">
      <el-select v-model="delegateTo" filterable style="width: 100%" placeholder="委托对方代审本步（一次性）">
        <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
      </el-select>
      <div class="hint">对方代审本步后委托即消耗；你的名义保留（动作记录"受你委托"）；你自己仍可直接审（先到先得）。</div>
      <template #footer>
        <el-button @click="delegateDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doDelegateAct">确认委派</el-button>
      </template>
    </el-dialog>

    <!-- 加签 -->
    <el-dialog v-model="addsignDlg" title="加签" width="480">
      <el-form label-width="90">
        <el-form-item label="位置">
          <el-radio-group v-model="addsignForm.position">
            <el-radio value="before">前加签（先审新步再回当前步）</el-radio>
            <el-radio value="after">后加签（当前步完成后先走新步）</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="签核人">
          <el-select v-model="addsignForm.approvers" multiple filterable style="width: 100%" placeholder="1-5 人">
            <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
          </el-select>
        </el-form-item>
        <el-form-item label="模式">
          <el-radio-group v-model="addsignForm.mode">
            <el-radio value="or-sign">或签（任一通过）</el-radio>
            <el-radio value="countersign">会签（全签）</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <div class="hint">加签即时生效（响应返回扩展后的步骤）；总步骤上限 20。</div>
      <template #footer>
        <el-button @click="addsignDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doAddsign">加签</el-button>
      </template>
    </el-dialog>

    <!-- 驳回至指定节点 -->
    <el-dialog v-model="rejectToDlg" title="驳回至指定节点" width="480">
      <el-form label-width="90">
        <el-form-item label="目标节点">
          <el-select v-model="rejectToTarget" style="width: 100%" placeholder="选择要退回的已过节点">
            <el-option v-for="i in rejectToChoices" :key="i" :value="i" :label="'第 ' + (i + 1) + ' 步：' + stepNameOf(i)" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明" required>
          <el-input v-model="rejectToComment" type="textarea" :rows="2" placeholder="退回原因（建议填写）" />
        </el-form-item>
      </el-form>
      <div class="hint">从目标步重走（Round+1）；已审动作按轮次失效，历史完整保留可审计。</div>
      <template #footer>
        <el-button @click="rejectToDlg = false">取消</el-button>
        <el-button type="danger" :loading="saving" @click="doRejectTo">驳回</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from '../element'
import type { TableColumnCtx } from 'element-plus'
import { ArrowDown } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'

// 契约 workflow-api-contract.md v1：六项审批操作 + 详情增量
type StepMode = 'countersign' | 'or-sign' | 'sequential'
interface WfStep { name: string; approvers: string[]; mode: StepMode }
interface WfDefRow { key: string; name: string; steps: WfStep[]; status: string }
interface UserRow { id: string; username: string }
interface InstanceRow {
  id: string
  business_key: string
  summary: string
  status: string
  created_at: string
  mode?: string
  round?: number
  cc_user_ids?: string[]
  delegations?: Array<{ step_index?: number; from: string; to: string; active: boolean }>
  steps?: WfStep[]
  step_index?: number
}
interface ActionItem { act: string; time: string; actor_id?: string; comment?: string; round?: number; delegate_from?: string }
interface InstanceDetail {
  instance?: InstanceRow
  actions?: ActionItem[]
}

const modeLabel = (m?: string) => ({ countersign: '会签', 'or-sign': '或签', sequential: '依次' }[m || ''] || m || '-')
const statusType = (s: string) => ({ approved: 'success', rejected: 'danger', canceled: 'info' }[s] || 'warning')
const actLabel = (a: string) => ({
  approve: '通过', reject: '驳回', cancel: '撤回', delegate: '转办',
  cc: '抄送', 'delegate-act': '委派', addsign: '加签', 'reject-to': '驳回至节点',
}[a] || a)
const actTag = (a: string): 'success' | 'danger' | 'warning' | 'info' | 'primary' => {
  const m: Record<string, 'success' | 'danger' | 'warning' | 'info' | 'primary'> = {
    approve: 'success', reject: 'danger', 'reject-to': 'danger',
    'delegate-act': 'warning', delegate: 'warning', addsign: 'primary', cc: 'info', cancel: 'info',
  }
  return m[a] || 'info'
}

const tab = ref('todo')
const todo = ref<InstanceRow[]>([])
const users = ref<UserRow[]>([])
const loading = ref(false)
const saving = ref(false)
const form = reactive<{ business_key: string; summary: string; approvers: string[]; mode: StepMode; defKey: string }>({
  business_key: '', summary: '', approvers: [], mode: 'countersign', defKey: '',
})
const defs = ref<WfDefRow[]>([])
const pickedSteps = ref<WfStep[]>([])
const listScope = ref('mine')
const instances = ref<InstanceRow[]>([])
const listLoading = ref(false)
const detailDlg = ref(false)
const detail = ref<InstanceDetail | null>(null)
const ccList = ref<InstanceRow[]>([])
const ccLoading = ref(false)

// 六操作对话框状态
const ccDlg = ref(false)
const ccTargets = ref<string[]>([])
const transferDlg = ref(false)
const transferTo = ref('')
const delegateDlg = ref(false)
const delegateTo = ref('')
const addsignDlg = ref(false)
const addsignForm = reactive<{ position: 'before' | 'after'; approvers: string[]; mode: 'or-sign' | 'countersign' }>({ position: 'before', approvers: [], mode: 'or-sign' })
const rejectToDlg = ref(false)
const rejectToTarget = ref(0)
const rejectToComment = ref('')
let actingRow: InstanceRow | null = null

const detailSteps = computed(() => detail.value?.instance?.steps || [])
const currentStepIndex = computed(() => detail.value?.instance?.step_index ?? -1)
// 驳回目标：0 .. 当前步-1（已过节点）
const rejectToChoices = computed(() => {
  const cur = detail.value?.instance?.step_index
  if (cur == null || cur < 1) return []
  return Array.from({ length: cur }, (_, i) => i)
})
const stepNameOf = (i: number) => detailSteps.value[i]?.name || '第 ' + (i + 1) + ' 步'
const userNameOf = (id?: string) => users.value.find((u) => u.id === id)?.username || (id ? id.slice(0, 8) : '-')

const fmtTime = (_r: InstanceRow, _c: TableColumnCtx<InstanceRow>, v: string) => new Date(v).toLocaleString('zh-CN')

async function loadDefs() {
  defs.value = await api<WfDefRow[]>('/api/v1/wf/definitions').catch(() => [])
}

function onDefPick(key: string) {
  pickedSteps.value = defs.value.find((d) => d.key === key)?.steps || []
}

async function loadTodo() {
  loading.value = true
  try {
    const [t, u] = await Promise.all([
      api<{ items: InstanceRow[] }>('/api/v1/wf/todo'),
      api<UserRow[]>('/api/v1/identity/users').catch(() => [] as UserRow[]),
    ])
    todo.value = t.items || []
    users.value = u
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function loadInstances() {
  listLoading.value = true
  try {
    const path = listScope.value === 'all' ? '/api/v1/wf/instances/all' : '/api/v1/wf/instances/mine'
    const d = await api<{ items: InstanceRow[] }>(path)
    instances.value = d.items || []
  } catch (e) {
    instances.value = []
    ElMessage.warning('无权查看全部实例（' + errMsg(e) + '）')
  } finally {
    listLoading.value = false
  }
}

// §2 抄送我的：实例快照列表（items/数组兼容，联调时按实际形态固化）
async function loadCc() {
  ccLoading.value = true
  try {
    const d = await api<InstanceRow[] | { items: InstanceRow[] }>('/api/v1/wf/cc')
    ccList.value = Array.isArray(d) ? d : d.items || []
  } catch {
    ccList.value = []
  } finally {
    ccLoading.value = false
  }
}

async function viewDetail(row: InstanceRow) {
  detail.value = await api<InstanceDetail>('/api/v1/wf/instances/' + row.id).catch(() => null)
  if (!detail.value) { ElMessage.error('无权查看详情'); return }
  actingRow = row
  detailDlg.value = true
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
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function act(row: InstanceRow, kind: 'approve' | 'reject') {
  try {
    await api(`/api/v1/wf/instances/${row.id}/${kind}`, { method: 'POST', body: { comment: kind === 'approve' ? '同意' : '驳回' } })
    ElMessage.success(kind === 'approve' ? '已通过' : '已驳回')
    await loadTodo()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

function moreAction(cmd: string, row: InstanceRow) {
  actingRow = row
  if (cmd === 'cc') { ccTargets.value = []; ccDlg.value = true }
  else if (cmd === 'transfer') { transferTo.value = ''; transferDlg.value = true }
  else if (cmd === 'delegate-act') { delegateTo.value = ''; delegateDlg.value = true }
  else if (cmd === 'addsign') { Object.assign(addsignForm, { position: 'before', approvers: [], mode: 'or-sign' }); addsignDlg.value = true }
  else if (cmd === 'reject-to') {
    rejectToTarget.value = 0
    rejectToComment.value = ''
    // 目标节点选择需当前步信息：先拉详情再开对话框
    api<InstanceDetail>('/api/v1/wf/instances/' + row.id).then((d) => {
      detail.value = d
      if (!rejectToChoices.value.length) {
        ElMessage.warning('当前无已过节点可退回（首步）')
        return
      }
      rejectToDlg.value = true
    }).catch(() => ElMessage.error('详情拉取失败'))
  }
}

async function doCc() {
  if (!actingRow || !ccTargets.value.length) return
  saving.value = true
  try {
    await api(`/api/v1/wf/instances/${actingRow.id}/cc`, { method: 'POST', body: { user_ids: ccTargets.value } })
    ElMessage.success('已抄送')
    ccDlg.value = false
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function doTransfer() {
  if (!actingRow || !transferTo.value) return
  saving.value = true
  try {
    await api(`/api/v1/wf/instances/${actingRow.id}/delegate`, { method: 'POST', body: { to_user_id: transferTo.value } })
    ElMessage.success('已转办')
    transferDlg.value = false
    await loadTodo()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function doDelegateAct() {
  if (!actingRow || !delegateTo.value) return
  saving.value = true
  try {
    await api(`/api/v1/wf/instances/${actingRow.id}/delegate-act`, { method: 'POST', body: { to_user_id: delegateTo.value } })
    ElMessage.success('已委派（对方可代审本步一次）')
    delegateDlg.value = false
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function doAddsign() {
  if (!actingRow) return
  if (!addsignForm.approvers.length || addsignForm.approvers.length > 5) {
    ElMessage.warning('签核人 1-5 人')
    return
  }
  saving.value = true
  try {
    await api(`/api/v1/wf/instances/${actingRow.id}/addsign`, { method: 'POST', body: { ...addsignForm } })
    ElMessage.success('已加签（步骤已扩展）')
    addsignDlg.value = false
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function doRejectTo() {
  if (!actingRow) return
  if (!rejectToComment.value.trim()) {
    ElMessage.warning('请填写退回说明')
    return
  }
  saving.value = true
  try {
    await api(`/api/v1/wf/instances/${actingRow.id}/reject-to`, {
      method: 'POST',
      body: { target_step: rejectToTarget.value, comment: rejectToComment.value },
    })
    ElMessage.success(`已驳回至第 ${rejectToTarget.value + 1} 步（第 N 轮重走）`)
    rejectToDlg.value = false
    await loadTodo()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

onMounted(() => { loadTodo(); loadInstances(); loadDefs() })
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; line-height: 1.6; }
</style>

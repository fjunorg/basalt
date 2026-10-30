<template>
  <div class="page">
    <el-tabs v-model="tab">
      <!-- 任务列表 -->
      <el-tab-pane label="任务列表" name="jobs">
        <div class="toolbar">
          <el-button v-if="can('job:manage')" type="primary" @click="openCreate">注册动态任务</el-button>
          <el-button @click="load">刷新</el-button>
          <span class="hint">内置任务注册制；动态任务为 HTTP 回调型（cron 与 interval 二选一，cron 优先），仅动态任务可删除</span>
        </div>
        <el-table :data="rows" border stripe v-loading="loading" size="small">
          <el-table-column prop="id" label="任务 ID" width="140" />
          <el-table-column prop="name" label="名称" width="130" />
          <el-table-column label="类型" width="70">
            <template #default="{ row }">
              <el-tag size="small" :type="row.kind === 'http' ? 'warning' : 'info'">{{ row.kind === 'http' ? '动态' : '内置' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="description" label="说明" min-width="140" show-overflow-tooltip />
          <el-table-column label="调度" width="120">
            <template #default="{ row }">
              <code style="font-size: 12px">{{ row.cron_expr || (row.interval_sec + 's') }}</code>
            </template>
          </el-table-column>
          <el-table-column label="目标" min-width="150" show-overflow-tooltip>
            <template #default="{ row }">
              <span v-if="row.kind === 'http'"><code style="font-size: 12px">{{ row.method }} {{ row.url }}</code></span>
              <span v-else style="color: var(--semi-color-text-3)">进程内函数</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="85">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '运行中' : '停用' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="上次结果" min-width="130">
            <template #default="{ row }">
              <el-tag v-if="row.last_err" type="danger" size="small">失败</el-tag>
              <span style="font-size: 12px; margin-left: 4px">{{ row.last_result || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="230" fixed="right">
            <template #default="{ row }">
              <el-button v-if="can('job:manage')" size="small" @click="toggle(row)">
                {{ row.enabled ? '停用' : '启用' }}
              </el-button>
              <el-button v-if="can('job:manage')" size="small" type="primary" @click="trigger(row)">立即执行</el-button>
              <el-button size="small" text @click="viewLogs(row)">日志</el-button>
              <el-button v-if="can('job:manage') && row.kind === 'http'" size="small" type="danger" @click="del(row)">删</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 执行日志 -->
      <el-tab-pane label="执行日志" name="logs">
        <div class="toolbar">
          <el-select v-model="logJob" filterable clearable placeholder="按任务过滤" size="small" style="width: 260px" @change="loadRuns(true)">
            <el-option v-for="j in rows" :key="j.id" :value="j.id" :label="j.id + '（' + j.name + '）'" />
          </el-select>
          <el-button size="small" @click="loadRuns(true)">刷新</el-button>
          <span class="hint">共 {{ runTotal }} 条记录（持久化，重启保留）</span>
        </div>
        <el-table :data="runItems" border stripe size="small" v-loading="runsLoading">
          <el-table-column label="时间" width="165">
            <template #default="{ row }">{{ new Date(row.start).toLocaleString('zh-CN') }}</template>
          </el-table-column>
          <el-table-column prop="job_id" label="任务" width="140" />
          <el-table-column label="结果" min-width="150">
            <template #default="{ row }">
              <el-tag v-if="row.err" type="danger" size="small">失败</el-tag>
              <el-tag v-else type="success" size="small">成功</el-tag>
              <span style="font-size: 12px; margin-left: 4px">{{ row.err || row.result || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="duration" label="耗时" width="100" />
        </el-table>
        <div class="toolbar" style="justify-content: flex-end" v-if="runTotal > runLimit">
          <el-button size="small" :disabled="runOffset === 0" @click="loadRuns(false, runOffset - runLimit)">上一页</el-button>
          <span class="hint">{{ runOffset / runLimit + 1 }} / {{ Math.ceil(runTotal / runLimit) }}</span>
          <el-button size="small" :disabled="runOffset + runLimit >= runTotal" @click="loadRuns(false, runOffset + runLimit)">下一页</el-button>
        </div>
        <el-empty v-if="!logJob" description="选择任务查看执行日志" :image-size="60" />
      </el-tab-pane>
    </el-tabs>

    <!-- 注册动态任务 -->
    <el-dialog v-model="dlg" title="注册 HTTP 动态任务" width="560">
      <el-form label-width="100">
        <el-form-item label="任务 ID" required>
          <el-input v-model="form.id" placeholder="如 webhook-report（唯一，创建后不可改）" />
        </el-form-item>
        <el-form-item label="名称" required><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="说明"><el-input v-model="form.description" /></el-form-item>
        <el-form-item label="调度方式" required>
          <el-radio-group v-model="form.scheduleType">
            <el-radio value="cron">Cron 表达式</el-radio>
            <el-radio value="interval">固定间隔</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.scheduleType === 'cron'" label="Cron">
          <el-input v-model="form.cron_expr" placeholder="标准 5 段（分 时 日 月 周），如 */5 2 * * *" />
          <div class="hint">支持 */n、a-b、列表；与 interval 二选一，同时填写时 cron 优先生效</div>
        </el-form-item>
        <el-form-item v-else label="间隔（秒）">
          <el-input-number v-model="form.interval_sec" :min="1" :step="10" />
        </el-form-item>
        <el-form-item label="回调 URL" required>
          <el-input v-model="form.url" placeholder="http://目标服务/path（合法绝对地址）" />
        </el-form-item>
        <el-form-item label="HTTP 方法">
          <el-select v-model="form.method" style="width: 200px">
            <el-option label="POST（默认）" value="POST" />
            <el-option label="GET" value="GET" />
            <el-option label="PUT" value="PUT" />
          </el-select>
        </el-form-item>
        <el-form-item label="负载">
          <el-input v-model="form.payload" type="textarea" :rows="3" placeholder="请求体（可空；POST/PUT 时作为 body 发送）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">注册</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

// API 依据 pkg/modules/job（982b64c：cron 调度/动态 HTTP 任务/执行日志）
interface JobRow {
  id: string
  name: string
  description?: string
  interval_sec: number
  cron_expr?: string
  kind: string // builtin | http
  url?: string
  method?: string
  enabled: boolean
  last_err?: boolean
  last_result?: string
}
interface RunRow { job_id: string; start: string; err?: string; result?: string; duration: string }

const can = (p: string) => hasPerm(perms.get(), p)

const tab = ref('jobs')
const rows = ref<JobRow[]>([])
const loading = ref(false)
const saving = ref(false)
const dlg = ref(false)
const logJob = ref('')
const runItems = ref<RunRow[]>([])
const runTotal = ref(0)
const runOffset = ref(0)
const runLimit = 20
const runsLoading = ref(false)
const form = reactive({
  id: '', name: '', description: '',
  scheduleType: 'interval' as 'cron' | 'interval',
  cron_expr: '', interval_sec: 60,
  url: '', method: 'POST', payload: '',
})

async function load() {
  loading.value = true
  try {
    rows.value = await api<JobRow[]>('/api/v1/jobs')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function loadRuns(reset = false, offset = 0) {
  if (!logJob.value) { runItems.value = []; runTotal.value = 0; return }
  if (reset) runOffset.value = 0
  else runOffset.value = offset
  runsLoading.value = true
  try {
    const q = `offset=${runOffset.value}&limit=${runLimit}`
    const d = await api<{ total: number; items: RunRow[] }>(`/api/v1/jobs/${logJob.value}/runs?${q}`)
    runItems.value = d.items || []
    runTotal.value = d.total || 0
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    runsLoading.value = false
  }
}

function viewLogs(row: JobRow) {
  tab.value = 'logs'
  logJob.value = row.id
  loadRuns(true)
}

function openCreate() {
  Object.assign(form, { id: '', name: '', description: '', scheduleType: 'interval', cron_expr: '', interval_sec: 60, url: '', method: 'POST', payload: '' })
  dlg.value = true
}

async function save() {
  if (!form.id || !form.name || !form.url) {
    ElMessage.warning('任务 ID、名称与回调 URL 必填')
    return
  }
  saving.value = true
  try {
    // cron 与 interval 二选一（后端 cron 优先语义：选 cron 时不传间隔）
    await api('/api/v1/jobs/http', {
      method: 'POST',
      body: {
        id: form.id, name: form.name, description: form.description,
        cron_expr: form.scheduleType === 'cron' ? form.cron_expr : '',
        interval_sec: form.scheduleType === 'interval' ? form.interval_sec : 0,
        url: form.url, method: form.method, payload: form.payload,
      },
    })
    ElMessage.success('已注册并开始调度')
    dlg.value = false
    await load()
  } catch (e) {
    // 400：cron 非法 / URL 非法 / 方法白名单 / ID 重复，按后端 message 提示
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function toggle(row: JobRow) {
  try {
    await api(`/api/v1/jobs/${row.id}/status`, {
      method: 'PATCH',
      body: { enabled: !row.enabled },
    })
    ElMessage.success(row.enabled ? '已停用' : '已启用')
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function trigger(row: JobRow) {
  try {
    await api(`/api/v1/jobs/${row.id}/trigger`, { method: 'POST' })
    ElMessage.success('已执行')
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function del(row: JobRow) {
  try {
    await ElMessageBox.confirm(`删除动态任务「${row.name}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/jobs/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    // 409：内置任务不可删（按钮已按 kind 隐藏，双保险）
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(load)
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; }
</style>

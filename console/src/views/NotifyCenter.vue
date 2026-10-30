<template>
  <div class="page">
    <el-tabs v-model="tab">
      <!-- 模板 -->
      <el-tab-pane label="通知模板" name="tpl">
        <div class="toolbar">
          <el-button v-if="can('notify:manage')" type="primary" @click="openTpl()">新建模板</el-button>
          <el-button @click="loadTpls">刷新</el-button>
        </div>
        <el-table :data="tpls" border stripe size="small" v-loading="loading">
          <el-table-column prop="code" label="Code" width="150" />
          <el-table-column label="渠道" width="90">
            <template #default="{ row }">
              <el-tag size="small" :type="chTag(row.channel)">{{ chLabel(row.channel) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="name" label="名称" width="160" />
          <el-table-column prop="subject" label="主题/标题" min-width="150" show-overflow-tooltip />
          <el-table-column prop="content" label="内容" min-width="200" show-overflow-tooltip />
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag size="small" :type="row.status === 'active' ? 'success' : 'info'">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="150" fixed="right">
            <template #default="{ row }">
              <el-button v-if="can('notify:manage')" size="small" @click="openTpl(row)">编辑</el-button>
              <el-button v-if="can('notify:manage')" size="small" type="danger" @click="delTpl(row)">删</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 邮件账号 -->
      <el-tab-pane label="邮件账号" name="smtp">
        <div class="toolbar">
          <el-button v-if="can('notify:manage')" type="primary" @click="smtpDlg = true">新建账号</el-button>
          <el-button @click="loadSMTPs">刷新</el-button>
          <span class="hint">密码经环境变量 BASALT_NOTIFY_SMTP_PWD_&lt;账号名大写&gt; 注入，不落库、不经前端</span>
        </div>
        <el-table :data="smtps" border stripe size="small" v-loading="loading">
          <el-table-column prop="name" label="账号名" width="140" />
          <el-table-column label="服务器" min-width="180">
            <template #default="{ row }">{{ row.host }}:{{ row.port }}{{ row.start_tls ? '（STARTTLS）' : '' }}</template>
          </el-table-column>
          <el-table-column prop="username" label="用户名" width="150" />
          <el-table-column prop="from" label="发件人" min-width="160" />
          <el-table-column label="操作" width="80" fixed="right">
            <template #default="{ row }">
              <el-button v-if="can('notify:manage')" size="small" type="danger" @click="delSMTP(row)">删</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 短信渠道 -->
      <el-tab-pane label="短信渠道" name="sms">
        <div class="toolbar">
          <el-button v-if="can('notify:manage')" type="primary" @click="smsDlg = true">新建渠道</el-button>
          <el-button @click="loadSMSs">刷新</el-button>
          <span class="hint">AccessKey/Secret 经环境变量 BASALT_NOTIFY_SMS_&lt;渠道名大写&gt;_KEY/_SECRET 注入</span>
        </div>
        <el-table :data="smss" border stripe size="small" v-loading="loading">
          <el-table-column prop="name" label="渠道名" width="150" />
          <el-table-column prop="provider" label="服务商" width="110" />
          <el-table-column prop="sign" label="签名" width="140" />
          <el-table-column prop="region" label="地域" width="120" />
          <el-table-column label="操作" width="80" fixed="right">
            <template #default="{ row }">
              <el-button v-if="can('notify:manage')" size="small" type="danger" @click="delSMS(row)">删</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 发送日志 -->
      <el-tab-pane label="发送日志" name="logs">
        <div class="toolbar">
          <el-select v-model="logChannel" clearable placeholder="按渠道过滤" size="small" style="width: 140px" @change="loadLogs(false)">
            <el-option label="短信" value="sms" />
            <el-option label="邮件" value="email" />
            <el-option label="站内信" value="inbox" />
          </el-select>
          <el-button @click="loadLogs(false)">刷新</el-button>
          <span class="hint">共 {{ logTotal }} 条；失败日志可手动重发（不自动重试）</span>
        </div>
        <el-table :data="logs" border stripe size="small" v-loading="loading">
          <el-table-column label="时间" width="165">
            <template #default="{ row }">{{ new Date(row.created_at).toLocaleString('zh-CN') }}</template>
          </el-table-column>
          <el-table-column label="渠道" width="80">
            <template #default="{ row }">
              <el-tag size="small" :type="chTag(row.channel)">{{ chLabel(row.channel) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="receiver" label="接收者" width="170" show-overflow-tooltip />
          <el-table-column prop="template_code" label="模板" width="130" />
          <el-table-column prop="content" label="内容" min-width="180" show-overflow-tooltip />
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag size="small" :type="row.status === 'sent' ? 'success' : 'danger'">{{ row.status }}</el-tag>
              <div v-if="row.err" class="hint">{{ row.err }}</div>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="90" fixed="right">
            <template #default="{ row }">
              <el-button v-if="row.status === 'failed' && can('notify:manage')" size="small" type="primary" text @click="resend(row)">重发</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div class="toolbar" style="justify-content: flex-end" v-if="logTotal > logLimit">
          <el-button size="small" :disabled="logOffset === 0" @click="loadLogs(false, logOffset - logLimit)">上一页</el-button>
          <span class="hint">{{ logOffset / logLimit + 1 }} / {{ Math.ceil(logTotal / logLimit) }}</span>
          <el-button size="small" :disabled="logOffset + logLimit >= logTotal" @click="loadLogs(false, logOffset + logLimit)">下一页</el-button>
        </div>
      </el-tab-pane>
    </el-tabs>

    <!-- 模板对话框 -->
    <el-dialog v-model="tplDlg" :title="tplEditing ? '编辑模板' : '新建模板'" width="560">
      <el-form label-width="90">
        <el-form-item label="Code" required>
          <el-input v-model="tplForm.code" :disabled="!!tplEditing" placeholder="如 welcome-email" />
        </el-form-item>
        <el-form-item label="渠道" required>
          <el-select v-model="tplForm.channel" style="width: 100%" :disabled="!!tplEditing">
            <el-option label="短信" value="sms" />
            <el-option label="邮件" value="email" />
            <el-option label="站内信" value="inbox" />
          </el-select>
        </el-form-item>
        <el-form-item label="名称"><el-input v-model="tplForm.name" /></el-form-item>
        <el-form-item label="主题/标题"><el-input v-model="tplForm.subject" placeholder="邮件主题 / 站内信标题（可空）" /></el-form-item>
        <el-form-item label="内容" required>
          <el-input v-model="tplForm.content" type="textarea" :rows="5" placeholder="支持 {{var}} 变量，未匹配变量保留原样" />
        </el-form-item>
        <el-form-item v-if="tplEditing" label="状态">
          <el-radio-group v-model="tplForm.status">
            <el-radio value="active">启用</el-radio>
            <el-radio value="disabled">停用</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="tplDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveTpl">保存</el-button>
      </template>
    </el-dialog>

    <!-- SMTP 对话框 -->
    <el-dialog v-model="smtpDlg" title="新建邮件账号" width="480">
      <el-form label-width="90">
        <el-form-item label="账号名" required><el-input v-model="smtpForm.name" placeholder="如 main（密码按此名注入环境变量）" /></el-form-item>
        <el-form-item label="服务器" required><el-input v-model="smtpForm.host" placeholder="smtp.example.com" /></el-form-item>
        <el-form-item label="端口" required><el-input-number v-model="smtpForm.port" :min="1" :max="65535" /></el-form-item>
        <el-form-item label="用户名"><el-input v-model="smtpForm.username" /></el-form-item>
        <el-form-item label="发件人" required><el-input v-model="smtpForm.from" placeholder="noreply@example.com" /></el-form-item>
        <el-form-item label="STARTTLS"><el-switch v-model="smtpForm.start_tls" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="smtpDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveSMTP">创建</el-button>
      </template>
    </el-dialog>

    <!-- SMS 对话框 -->
    <el-dialog v-model="smsDlg" title="新建短信渠道" width="480">
      <el-form label-width="90">
        <el-form-item label="渠道名" required><el-input v-model="smsForm.name" placeholder="如 main（Key/Secret 按此名注入）" /></el-form-item>
        <el-form-item label="服务商" required>
          <el-select v-model="smsForm.provider" style="width: 100%">
            <el-option label="阿里云" value="aliyun" />
            <el-option label="腾讯云" value="tencent" />
          </el-select>
        </el-form-item>
        <el-form-item label="签名" required><el-input v-model="smsForm.sign" /></el-form-item>
        <el-form-item label="地域"><el-input v-model="smsForm.region" placeholder="可空" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="smsDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveSMS">创建</el-button>
      </template>
    </el-dialog>

    <!-- 发送测试对话框 -->
    <el-dialog v-model="sendDlg" title="发送通知" width="480">
      <el-form label-width="90">
        <el-form-item label="渠道" required>
          <el-select v-model="sendForm.channel" style="width: 100%">
            <el-option label="短信" value="sms" />
            <el-option label="邮件" value="email" />
            <el-option label="站内信" value="inbox" />
          </el-select>
        </el-form-item>
        <el-form-item label="模板" required>
          <el-select v-model="sendForm.template_code" style="width: 100%">
            <el-option v-for="t in tpls" :key="t.code" :value="t.code" :label="t.code + '（' + t.name + '）'" :disabled="t.status !== 'active'" />
          </el-select>
        </el-form-item>
        <el-form-item label="接收者" required>
          <el-input v-model="sendForm.receiver" placeholder="手机号 / 邮箱 / 用户 ID（站内信）" />
        </el-form-item>
        <el-form-item label="变量">
          <el-input v-model="sendForm.varsText" type="textarea" :rows="3" placeholder='JSON 对象，如 {"name":"张三"}' />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="sendDlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doSend">发送</el-button>
      </template>
    </el-dialog>

    <div v-if="can('notify:manage')" style="position: fixed; right: 32px; bottom: 32px">
      <el-button type="primary" circle size="large" @click="sendDlg = true">发</el-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

// API 依据 pkg/modules/notify 实际路由与实体（module.go/notify.go）
interface TplRow { id: string; code: string; channel: string; name: string; subject?: string; content: string; status: string }
interface SMTPRow { id: string; name: string; host: string; port: number; username: string; from: string; start_tls: boolean }
interface SMSRow { id: string; name: string; provider: string; sign: string; region?: string }
interface LogRow { id: string; channel: string; receiver: string; template_code: string; content: string; status: string; err?: string; created_at: string }

const can = (p: string) => hasPerm(perms.get(), p)
const chLabel = (c: string) => ({ sms: '短信', email: '邮件', inbox: '站内信' }[c] || c)
const chTag = (c: string) => ({ sms: 'warning', email: 'primary', inbox: 'success' }[c] || 'info')

const tab = ref('tpl')
const loading = ref(false)
const saving = ref(false)
const tpls = ref<TplRow[]>([])
const smtps = ref<SMTPRow[]>([])
const smss = ref<SMSRow[]>([])
const logs = ref<LogRow[]>([])
const logChannel = ref('')
const logOffset = ref(0)
const logLimit = 50
const logTotal = ref(0)

const tplDlg = ref(false)
const smtpDlg = ref(false)
const smsDlg = ref(false)
const sendDlg = ref(false)
const tplEditing = ref<TplRow | null>(null)
const tplForm = reactive({ code: '', channel: 'email', name: '', subject: '', content: '', status: 'active' })
const smtpForm = reactive({ name: '', host: '', port: 465, username: '', from: '', start_tls: true })
const smsForm = reactive({ name: '', provider: 'aliyun', sign: '', region: '' })
const sendForm = reactive({ channel: 'email', template_code: '', receiver: '', varsText: '' })

async function loadTpls() { tpls.value = await api<TplRow[]>('/api/v1/notify/templates').catch(() => []) }
async function loadSMTPs() { smtps.value = await api<SMTPRow[]>('/api/v1/notify/smtp-accounts').catch(() => []) }
async function loadSMSs() { smss.value = await api<SMSRow[]>('/api/v1/notify/sms-channels').catch(() => []) }

async function loadLogs(switchTab = true, offset = 0) {
  if (switchTab) logOffset.value = 0
  else logOffset.value = offset
  loading.value = true
  try {
    const q = new URLSearchParams({ offset: String(logOffset.value), limit: String(logLimit) })
    if (logChannel.value) q.set('channel', logChannel.value)
    const d = await api<{ items: LogRow[]; total: number }>('/api/v1/notify/logs?' + q)
    logs.value = d.items || []
    logTotal.value = d.total || 0
  } finally {
    loading.value = false
  }
}

function openTpl(row?: TplRow) {
  tplEditing.value = row || null
  Object.assign(tplForm, row
    ? { code: row.code, channel: row.channel, name: row.name, subject: row.subject || '', content: row.content, status: row.status }
    : { code: '', channel: 'email', name: '', subject: '', content: '', status: 'active' })
  tplDlg.value = true
}

async function saveTpl() {
  if (!tplForm.code || !tplForm.content) {
    ElMessage.warning('Code 与内容必填')
    return
  }
  saving.value = true
  try {
    if (tplEditing.value) {
      // PATCH 可变子集：name/subject/content/status（后端语义）
      await api(`/api/v1/notify/templates/${tplEditing.value.id}`, {
        method: 'PATCH',
        body: { name: tplForm.name, subject: tplForm.subject, content: tplForm.content, status: tplForm.status },
      })
    } else {
      await api('/api/v1/notify/templates', { method: 'POST', body: { ...tplForm } })
    }
    ElMessage.success('已保存')
    tplDlg.value = false
    await loadTpls()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function delTpl(row: TplRow) {
  try {
    await ElMessageBox.confirm(`删除模板 ${row.code}？`, '确认', { type: 'warning' })
    await api(`/api/v1/notify/templates/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await loadTpls()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

async function saveSMTP() {
  if (!smtpForm.name || !smtpForm.host || !smtpForm.from) {
    ElMessage.warning('账号名/服务器/发件人必填')
    return
  }
  saving.value = true
  try {
    await api('/api/v1/notify/smtp-accounts', { method: 'POST', body: { ...smtpForm } })
    ElMessage.success('已创建（密码请在服务端配置对应环境变量）')
    smtpDlg.value = false
    Object.assign(smtpForm, { name: '', host: '', port: 465, username: '', from: '', start_tls: true })
    await loadSMTPs()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function delSMTP(row: SMTPRow) {
  try {
    await ElMessageBox.confirm(`删除邮件账号「${row.name}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/notify/smtp-accounts/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await loadSMTPs()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

async function saveSMS() {
  if (!smsForm.name || !smsForm.sign) {
    ElMessage.warning('渠道名与签名必填')
    return
  }
  saving.value = true
  try {
    await api('/api/v1/notify/sms-channels', { method: 'POST', body: { ...smsForm } })
    ElMessage.success('已创建（Key/Secret 请在服务端配置对应环境变量）')
    smsDlg.value = false
    Object.assign(smsForm, { name: '', provider: 'aliyun', sign: '', region: '' })
    await loadSMSs()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function delSMS(row: SMSRow) {
  try {
    await ElMessageBox.confirm(`删除短信渠道「${row.name}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/notify/sms-channels/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await loadSMSs()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

async function resend(row: LogRow) {
  try {
    await api(`/api/v1/notify/resend/${row.id}`, { method: 'POST' })
    ElMessage.success('已重发，结果见日志')
    await loadLogs(false, logOffset.value)
  } catch (e) {
    // 422 携带失败原因与新日志 ID
    ElMessage.error(errMsg(e))
    await loadLogs(false, logOffset.value)
  }
}

async function doSend() {
  if (!sendForm.channel || !sendForm.receiver || !sendForm.template_code) {
    ElMessage.warning('渠道/模板/接收者必填')
    return
  }
  let vars: Record<string, string> = {}
  if (sendForm.varsText.trim()) {
    try {
      vars = JSON.parse(sendForm.varsText)
    } catch {
      ElMessage.warning('变量须为合法 JSON 对象')
      return
    }
  }
  saving.value = true
  try {
    await api('/api/v1/notify/send', {
      method: 'POST',
      body: { channel: sendForm.channel, receiver: sendForm.receiver, template_code: sendForm.template_code, vars },
    })
    ElMessage.success('已发送，结果见日志')
    sendDlg.value = false
    tab.value = 'logs'
    await loadLogs()
  } catch (e) {
    // 422 = 业务失败（渠道未配置/凭据缺失等），message 含失败原因与日志 ID
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

onMounted(() => { loadTpls(); loadSMTPs(); loadSMSs(); loadLogs() })
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; line-height: 1.6; }
</style>

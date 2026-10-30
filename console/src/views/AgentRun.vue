<template>
  <div class="page run-wrap">
    <el-card class="side" shadow="never">
      <template #header>Agent 运行</template>
      <el-select v-model="agentId" placeholder="选择 Agent" style="width: 100%" size="small" @change="onAgentChange">
        <el-option v-for="a in agents" :key="a.id" :value="a.id" :label="a.name" :disabled="a.status !== 'active'" />
      </el-select>
      <el-divider content-position="left">历史会话</el-divider>
      <div class="sessions">
        <div v-for="s in sessions" :key="s.id" class="session" :class="{ active: s.id === sessionId }" @click="pickSession(s)">
          <div class="s-title">{{ s.input }}</div>
          <div class="s-time">
            <span>{{ s.started_at ? new Date(s.started_at).toLocaleString('zh-CN') : '' }}</span>
            <el-tag size="small" :type="statusTag(s.status)">{{ s.status }}</el-tag>
          </div>
        </div>
        <el-empty v-if="!sessions.length" description="暂无历史会话" :image-size="48" />
      </div>
    </el-card>

    <div class="main">
      <el-card shadow="never" class="chat-card">
        <template #header>
          对话{{ currentAgent ? ` · ${currentAgent.name}` : '' }}
          <el-tag v-if="running" size="small" type="warning" style="float: right">运行中…</el-tag>
        </template>
        <div ref="box" class="chat-box">
          <div v-for="(m, i) in messages" :key="i" :class="['msg', m.role]">
            <!-- role=tool：工具结果消息，浅色小卡片带工具名 -->
            <div v-if="m.role === 'tool'" class="tool-msg">
              <el-tag size="small" type="warning">{{ m.tool_name || 'tool' }}</el-tag>
              <span class="tool-text">{{ m.content }}</span>
            </div>
            <template v-else>
              <div class="bubble">{{ m.content }}</div>
              <!-- assistant 消息附带的工具调用（历史回放时可见） -->
              <el-tag v-for="tc in m.tool_calls || []" :key="tc.call_id" size="small" type="warning"
                style="align-self: flex-start; margin: 2px 0">⚙ {{ tc.name }}</el-tag>
            </template>
          </div>
          <el-empty v-if="!messages.length" description="选择 Agent 与会话，输入消息开始运行" :image-size="60" />
        </div>
        <div class="input-bar">
          <el-input v-model="input" placeholder="输入消息，Enter 发送" @keyup.enter="send" :disabled="running || !agentId" />
          <el-button type="primary" :loading="running" :disabled="!agentId" @click="send">发送</el-button>
        </div>
      </el-card>

      <el-card shadow="never" class="event-card">
        <template #header>运行事件（工具调用过程）</template>
        <el-empty v-if="!events.length" description="运行事件将在此流式展示" :image-size="48" />
        <el-timeline v-else>
          <el-timeline-item v-for="(e, i) in events" :key="i" :type="e.tag" :timestamp="e.time">
            <b>{{ e.title }}</b>
            <div v-if="e.detail" class="ev-detail">{{ e.detail }}</div>
          </el-timeline-item>
        </el-timeline>
      </el-card>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from '../element'
import { api, agentRunStream, errMsg, type AgentDef, type AgentRun, type AgentRunMessage, type AgentSseEvent } from '../api'

// 页面级 agent:run（路由 meta.perms）；选择器/历史数据端点需 agent:read
interface ChatMsg {
  role: 'user' | 'assistant' | 'tool'
  content: string
  tool_calls?: AgentRunMessage['tool_calls']
  tool_name?: string
}
interface EventItem { title: string; detail?: string; time: string; tag: 'primary' | 'success' | 'danger' | 'warning' | 'info' }

const now = () => new Date().toLocaleTimeString('zh-CN')
const statusTag = (s: string) => ({ completed: 'success', failed: 'danger', safety_rejected: 'danger', running: 'warning' }[s] || 'info')

const agents = ref<AgentDef[]>([])
const agentId = ref('')
const sessions = ref<AgentRun[]>([])
const sessionId = ref('')
const messages = ref<ChatMsg[]>([])
const events = ref<EventItem[]>([])
const input = ref('')
const running = ref(false)
const box = ref<HTMLElement | null>(null)

const currentAgent = computed(() => agents.value.find((a) => a.id === agentId.value) || null)

async function loadAgents() {
  try {
    const d = await api<{ count: number; items: AgentDef[] }>('/api/v1/agent/agents')
    agents.value = d.items || []
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function loadSessions() {
  if (!agentId.value) return
  const d = await api<{ count: number; items: AgentRun[] }>(`/api/v1/agent/agents/${agentId.value}/runs`).catch(() => ({ items: [] as AgentRun[] }))
  sessions.value = d.items || []
}

function onAgentChange() {
  sessionId.value = ''
  messages.value = []
  events.value = []
  loadSessions()
}

async function pickSession(s: AgentRun) {
  sessionId.value = s.id
  try {
    // §3.3 运行详情：完整消息历史回放（user/assistant/tool 三角色）
    const d = await api<{ run: AgentRun; messages: AgentRunMessage[] }>(`/api/v1/agent/runs/${s.id}`)
    messages.value = d.messages.map((m) => ({ role: m.role, content: m.content, tool_calls: m.tool_calls, tool_name: m.tool_name }))
    events.value = []
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

function pushEvent(title: string, tag: EventItem['tag'], detail?: string) {
  events.value.push({ title, tag, detail, time: now() })
}

const scrollToBottom = () => box.value?.scrollTo({ top: box.value.scrollHeight })

async function send() {
  const text = input.value.trim()
  if (!text || running.value || !agentId.value) return
  input.value = ''
  messages.value.push({ role: 'user', content: text })
  const reply: ChatMsg = { role: 'assistant', content: '' }
  running.value = true
  events.value = []
  let replyPushed = false
  try {
    await agentRunStream(agentId.value, text, (ev: AgentSseEvent) => {
      const d = ev.data
      switch (ev.event) {
        case 'run.started':
          pushEvent('运行开始', 'info', `model: ${d.model || '-'}`)
          break
        case 'tool.call':
          pushEvent(`调用工具 ${d.name || ''}`, 'primary', d.arguments)
          break
        case 'tool.result':
          pushEvent(`工具返回 ${d.name || ''}（${d.ok ? '成功' : '失败'}）`, d.ok ? 'success' : 'danger', d.summary || (d.result || '').slice(0, 200))
          break
        case 'llm.delta': {
          if (!replyPushed) { messages.value.push(reply); replyPushed = true }
          reply.content += d.text || ''
          scrollToBottom()
          break
        }
        case 'run.completed':
          pushEvent('运行完成', 'success', `rounds: ${d.rounds ?? '-'} · tokens: ${d.total_tokens ?? '-'}`)
          break
        case 'run.failed':
          pushEvent('运行失败', 'danger', d.error)
          reply.content = reply.content || ('⚠ ' + (d.error || '运行失败'))
          ElMessage.error(d.error || '运行失败')
          break
        case 'safety.rejected':
          pushEvent(`内容安全拦截（${d.side === 'input' ? '输入侧' : '输出侧'}）`, 'danger', d.message)
          reply.content = reply.content || ('⚠ 内容安全拦截：' + (d.message || ''))
          ElMessage.warning('内容安全拦截：' + (d.message || ''))
          break
      }
    })
    if (reply.content && !replyPushed) { messages.value.push(reply); replyPushed = true }
  } catch (e) {
    // SSE 握手阶段错误（普通 JSON 信封）：401/403/404/409 等
    ElMessage.error(errMsg(e))
  } finally {
    running.value = false
    scrollToBottom()
    await loadSessions()
  }
}

onMounted(loadAgents)
</script>

<style scoped>
.run-wrap { display: flex; gap: 16px; align-items: stretch; }
.side { width: 280px; flex-shrink: 0; }
.sessions { max-height: 480px; overflow-y: auto; }
.session { padding: 8px; border-radius: 6px; cursor: pointer; }
.session:hover { background: #f5f7fa; }
.session.active { background: #ecf5ff; }
.s-title { font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.s-time { font-size: 12px; color: #909399; margin-top: 2px; display: flex; gap: 6px; align-items: center; }
.main { flex: 1; display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.chat-card { flex: 1; }
.chat-box { height: 300px; overflow-y: auto; padding: 4px; }
.msg { display: flex; flex-direction: column; margin: 8px 0; }
.msg.user { align-items: flex-end; }
.bubble { max-width: 72%; padding: 8px 12px; border-radius: 8px; white-space: pre-wrap; background: #f4f4f5; line-height: 1.6; }
.msg.user .bubble { background: #409eff; color: #fff; }
.tool-msg { max-width: 80%; padding: 6px 10px; border-radius: 8px; background: #fdf6ec; font-size: 12px; display: flex; gap: 6px; align-items: flex-start; }
.tool-text { color: #b88230; word-break: break-all; max-height: 90px; overflow-y: auto; }
.input-bar { display: flex; gap: 8px; margin-top: 12px; }
.event-card { max-height: 320px; overflow-y: auto; }
.ev-detail { color: #606266; font-size: 12px; margin-top: 2px; word-break: break-all; }
</style>

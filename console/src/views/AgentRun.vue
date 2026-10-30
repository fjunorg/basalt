<template>
  <div class="page run-wrap">
    <el-card class="side" shadow="never">
      <template #header>Agent 运行（骨架）</template>
      <el-select v-model="agentId" placeholder="选择 Agent" style="width: 100%" size="small">
        <el-option v-for="a in agents" :key="a.id" :value="a.id" :label="a.name" />
      </el-select>
      <el-divider content-position="left">历史会话</el-divider>
      <div class="sessions">
        <div v-for="s in sessions" :key="s.id" class="session" :class="{ active: s.id === sessionId }" @click="pickSession(s)">
          <div class="s-title">{{ s.title }}</div>
          <div class="s-time">{{ s.time }}</div>
        </div>
        <el-empty v-if="!sessions.length" description="暂无历史会话" :image-size="48" />
      </div>
    </el-card>

    <div class="main">
      <el-card shadow="never" class="chat-card">
        <template #header>
          对话
          <el-tag v-if="running" size="small" type="warning" style="float: right">运行中…</el-tag>
        </template>
        <div ref="box" class="chat-box">
          <div v-for="(m, i) in messages" :key="i" :class="['msg', m.role]">
            <div class="bubble">{{ m.content }}</div>
          </div>
          <el-empty v-if="!messages.length" description="选择 Agent 与会话，输入消息开始运行" :image-size="60" />
        </div>
        <div class="input-bar">
          <el-input v-model="input" placeholder="输入消息，Enter 发送" @keyup.enter="send" :disabled="running || !agentId" />
          <el-button type="primary" :loading="running" :disabled="!agentId" @click="send">发送</el-button>
        </div>
      </el-card>

      <el-card shadow="never" class="event-card">
        <template #header>运行事件（工具调用过程可观测）</template>
        <el-empty v-if="!events.length" description="运行事件将在此流式展示" :image-size="48" />
        <el-timeline v-else>
          <el-timeline-item v-for="(e, i) in events" :key="i" :type="eventTag(e.kind)" :timestamp="e.time">
            <b>{{ e.kind }}</b>
            <span v-if="e.detail" style="color: #606266; font-size: 12px">　{{ e.detail }}</span>
          </el-timeline-item>
        </el-timeline>
      </el-card>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from '../element'

// 骨架阶段结构为前端预期占位；SSE 事件格式以 agent-api-contract.md 落地后为准
interface AgentOption { id: string; name: string }
interface SessionRow { id: string; title: string; time: string }
interface RunEvent { kind: string; detail?: string; time: string }

const agentId = ref('')
const agents = ref<AgentOption[]>([])
const sessionId = ref('')
const sessions = ref<SessionRow[]>([])
const messages = ref<Array<{ role: 'user' | 'assistant'; content: string }>>([])
const events = ref<RunEvent[]>([])
const input = ref('')
const running = ref(false)
const box = ref<HTMLElement | null>(null)

const eventTag = (kind: string) =>
  ({ tool_call: 'primary', tool_result: 'success', error: 'danger', done: 'success', step: 'warning' }[kind] || 'info')

function pickSession(s: SessionRow) {
  sessionId.value = s.id
  // TODO(contract): 会话详情/消息回放端点待契约确认后对接
}

async function send() {
  const text = input.value.trim()
  if (!text || running.value || !agentId.value) return
  // TODO(contract): SSE 运行端点与事件解析待 agent-api-contract.md 确认后对接
  ElMessage.info('等待 API 契约对接后启用')
}
</script>

<style scoped>
.run-wrap { display: flex; gap: 16px; align-items: stretch; }
.side { width: 260px; flex-shrink: 0; }
.sessions { max-height: 420px; overflow-y: auto; }
.session { padding: 8px; border-radius: 6px; cursor: pointer; }
.session:hover { background: #f5f7fa; }
.session.active { background: #ecf5ff; }
.s-title { font-size: 13px; }
.s-time { font-size: 12px; color: #909399; }
.main { flex: 1; display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.chat-card { flex: 1; }
.chat-box { height: 300px; overflow-y: auto; padding: 4px; }
.msg { display: flex; margin: 8px 0; }
.msg.user { justify-content: flex-end; }
.bubble { max-width: 72%; padding: 8px 12px; border-radius: 8px; white-space: pre-wrap; background: #f4f4f5; line-height: 1.6; }
.msg.user .bubble { background: #409eff; color: #fff; }
.input-bar { display: flex; gap: 8px; margin-top: 12px; }
.event-card { max-height: 320px; overflow-y: auto; }
</style>

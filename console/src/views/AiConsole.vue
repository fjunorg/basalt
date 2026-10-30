<template>
  <div class="page">
    <el-row :gutter="16">
      <el-col :span="16">
        <el-card>
          <template #header>
            对话（OpenAI 兼容 · 流式）
            <el-select v-model="model" size="small" style="width: 200px; float: right">
              <el-option v-for="p in providers" :key="p" :value="p" :label="p" />
            </el-select>
          </template>
          <div ref="box" class="chat-box">
            <div v-for="(m, i) in messages" :key="i" :class="['msg', m.role]">
              <div class="bubble">{{ m.content }}</div>
            </div>
            <el-empty v-if="!messages.length" description="发起新对话" :image-size="60" />
          </div>
          <div style="display: flex; gap: 8px; margin-top: 12px">
            <el-input v-model="input" placeholder="输入消息，Enter 发送" @keyup.enter="send" :disabled="streaming" />
            <el-button type="primary" :loading="streaming" @click="send">发送</el-button>
            <el-button @click="messages = []">清空</el-button>
          </div>
        </el-card>
      </el-col>
      <el-col :span="8">
        <el-card>
          <template #header>本账期用量</template>
          <el-progress type="circle" :percentage="pct" :status="pct >= 100 ? 'exception' : undefined" />
          <div style="margin-top: 12px; color: #606266; font-size: 13px">
            已用 <b>{{ quota?.used ?? 0 }}</b> / {{ quota?.limit === -1 ? '不限' : quota?.limit ?? '-' }} tokens
          </div>
          <el-divider />
          <div style="font-size: 13px; color: #909399">
            配额按月度账期自动重置；超额后调用返回 429。
          </div>
          <template v-if="can('ai:quota:manage')">
            <el-divider content-position="left">配额管理</el-divider>
            <el-select v-model="quotaTenant" placeholder="选择租户" size="small" style="width: 100%" @change="loadTenantQuota">
              <el-option label="platform（平台级用户）" value="platform" />
              <el-option v-for="t in tenants" :key="t.id" :value="t.id" :label="t.code + '（' + t.name + '）'" />
            </el-select>
            <div v-if="tenantQuota" style="margin-top: 8px; font-size: 13px">
              当前：已用 <b>{{ tenantQuota.used }}</b> / {{ tenantQuota.limit === -1 ? '不限' : tenantQuota.limit }}
              <el-button size="small" style="margin-left: 8px" @click="loadTenantQuota">刷新</el-button>
            </div>
            <div style="display: flex; gap: 8px; margin-top: 8px">
              <el-input-number v-model="newLimit" :min="-1" :step="10000" size="small" style="flex: 1"
                placeholder="新限额（-1 不限）" />
              <el-button type="primary" size="small" @click="saveQuota">保存</el-button>
            </div>
          </template>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from '../element'
import { api, chatStream, perms, hasPerm, errMsg, type ChatMessage } from '../api'

interface QuotaInfo { used: number; limit: number }
interface TenantRow { id: string; code: string; name: string }

const can = (p: string) => hasPerm(perms.get(), p)

const messages = ref<ChatMessage[]>([])
const input = ref('')
const streaming = ref(false)
const model = ref('')
const providers = ref<string[]>([])
const quota = ref<QuotaInfo | null>(null)
const box = ref<HTMLElement | null>(null)
const tenants = ref<TenantRow[]>([])
const quotaTenant = ref('')
const tenantQuota = ref<QuotaInfo | null>(null)
const newLimit = ref(1000000)

async function loadTenantQuota() {
  if (!quotaTenant.value) return
  tenantQuota.value = await api<QuotaInfo>('/api/v1/ai/quota/' + quotaTenant.value).catch(() => null)
  if (tenantQuota.value && tenantQuota.value.limit >= 0) newLimit.value = tenantQuota.value.limit
}

async function saveQuota() {
  try {
    await api('/api/v1/ai/quota/' + quotaTenant.value, {
      method: 'PUT',
      body: { limit: newLimit.value },
    })
    ElMessage.success('已保存（' + (newLimit.value < 0 ? '不限量' : newLimit.value) + '）')
    await loadTenantQuota()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

const pct = computed(() => {
  if (!quota.value || quota.value.limit < 0) return 0
  return Math.min(100, Math.round((quota.value.used / Math.max(quota.value.limit, 1)) * 100))
})

async function loadMeta() {
  providers.value = (await api<{ providers: string[] }>('/api/v1/ai/providers').catch(() => ({ providers: [] }))).providers || []
  if (providers.value.length) model.value = model.value || providers.value[0]
  quota.value = await api<QuotaInfo>('/api/v1/ai/quota/platform').catch(() => null)
}

async function send() {
  const text = input.value.trim()
  if (!text || streaming.value) return
  input.value = ''
  messages.value.push({ role: 'user', content: text })
  const reply: ChatMessage = { role: 'assistant', content: '' }
  messages.value.push(reply)
  streaming.value = true
  try {
    await chatStream({
      model: model.value || 'default',
      messages: messages.value.slice(0, -1).map(({ role, content }) => ({ role, content })),
      onDelta: (d) => {
        reply.content += d
        box.value?.scrollTo({ top: box.value.scrollHeight })
      },
    })
    quota.value = await api<QuotaInfo>('/api/v1/ai/quota/platform').catch(() => quota.value)
  } catch (e) {
    reply.content = reply.content || ('⚠ ' + errMsg(e))
  } finally {
    streaming.value = false
  }
}

onMounted(async () => {
  await loadMeta()
  tenants.value = await api<TenantRow[]>('/api/v1/tenant/tenants').catch(() => [])
})
</script>

<style scoped>
.chat-box { height: 420px; overflow-y: auto; padding: 4px; }
.msg { display: flex; margin: 8px 0; }
.msg.user { justify-content: flex-end; }
.bubble {
  max-width: 72%; padding: 8px 12px; border-radius: 8px; white-space: pre-wrap;
  background: #f4f4f5; line-height: 1.6;
}
.msg.user .bubble { background: #409eff; color: #fff; }
</style>

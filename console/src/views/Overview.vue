<template>
  <div class="page">
    <el-row :gutter="16">
      <el-col :span="8">
        <el-card>
          <template #header>我的身份</template>
          <p>用户名：<b>{{ me?.username }}</b></p>
          <p>用户 ID：<code style="font-size: 12px">{{ me?.id }}</code></p>
          <p>权限数：{{ me?.permissions?.length ?? '-' }}
            <el-tag v-for="p in (me?.permissions || []).slice(0, 5)" :key="p" size="small" style="margin-left: 4px">{{ p }}</el-tag>
            <el-tag v-if="(me?.permissions?.length || 0) > 5" size="small" type="info">…</el-tag>
          </p>
        </el-card>
      </el-col>
      <el-col :span="8">
        <el-card>
          <template #header>AI 用量（本账期）</template>
          <el-statistic :value="quota?.used ?? 0" title="已用 tokens" />
          <el-divider style="margin: 8px 0" />
          <span style="color: var(--semi-color-text-2); font-size: 13px">限额：{{ quota?.limit === -1 ? '不限' : quota?.limit ?? '-' }}</span>
        </el-card>
      </el-col>
      <el-col :span="8">
        <el-card>
          <template #header>审计链完整性</template>
          <el-result v-if="verify" :icon="verify.ok ? 'success' : 'error'"
            :title="verify.ok ? '链完整' : '链断裂 @ ' + verify.break_at"
            :sub-title="`算法 SM3/SHA-256 · 累计 ${verify.total_events} 事件`" style="padding: 8px 0" />
        </el-card>
      </el-col>
    </el-row>
    <el-card style="margin-top: 16px">
      <template #header>已注册模块（动态装配）</template>
      <el-tag v-for="m in modules" :key="m.name" style="margin: 4px" :type="m.name === 'identity' ? 'primary' : 'info'">
        {{ m.name }} v{{ m.version }}<span v-if="m.permissions?.length"> · {{ m.permissions.length }} 权限点</span>
      </el-tag>
    </el-card>
    <el-card style="margin-top: 16px">
      <template #header>提供方（OpenAI 兼容）</template>
      <el-tag v-for="p in providers?.providers || []" :key="p" style="margin-right: 8px">
        {{ p }}{{ p === providers?.default ? '（默认）' : '' }}
      </el-tag>
      <el-empty v-if="!(providers?.providers || []).length" description="未注册 AI 提供方（设置 BASALT_AI_PROVIDER_URL）" :image-size="48" />
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type MeInfo } from '../api'

interface QuotaInfo { used: number; limit: number }
interface VerifyInfo { ok: boolean; break_at?: string; total_events: number }
interface ProvidersInfo { providers: string[]; default?: string }
interface ModuleInfo { name: string; version: string; permissions?: string[] }

const me = ref<MeInfo | null>(null)
const quota = ref<QuotaInfo | null>(null)
const verify = ref<VerifyInfo | null>(null)
const providers = ref<ProvidersInfo | null>(null)
const modules = ref<ModuleInfo[]>([])

onMounted(async () => {
  me.value = await api<MeInfo>('/api/v1/identity/me').catch(() => null)
  quota.value = await api<QuotaInfo>('/api/v1/ai/quota/platform').catch(() => null)
  verify.value = await api<VerifyInfo>('/api/v1/audit/verify').catch(() => null)
  providers.value = await api<ProvidersInfo>('/api/v1/ai/providers').catch(() => null)
  modules.value = await api<ModuleInfo[]>('/api/v1/platform/modules').catch(() => [])
})
</script>

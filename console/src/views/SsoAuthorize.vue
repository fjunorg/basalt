<template>
  <div class="authz-wrap">
    <el-card class="authz-card" v-loading="loading">
      <template #header>授权确认</template>
      <template v-if="desc">
        <p style="margin: 0 0 4px">
          应用 <b>{{ desc.client_name }}</b> 请求访问你的账号，授权以下权限：
        </p>
        <div style="margin: 12px 0">
          <el-tag v-for="s in desc.scopes || []" :key="s" style="margin: 4px">{{ s }}</el-tag>
          <span v-if="!(desc.scopes || []).length" class="hint">（未声明具体 scope，仅基础身份）</span>
        </div>
        <el-divider />
        <p class="hint">授权后将跳转回：<code style="font-size: 12px">{{ desc.redirect_uri }}</code></p>
        <p class="hint">仅授权一次；应用后续可用 refresh token 续期（可随时由管理员吊销）。</p>
        <div style="display: flex; gap: 12px; margin-top: 20px">
          <el-button type="primary" :loading="submitting" @click="decide('approve')">同意授权</el-button>
          <el-button :loading="submitting" @click="decide('deny')">拒绝</el-button>
        </div>
      </template>
      <el-result v-else-if="errorText" icon="error" title="授权请求无效" :sub-title="errorText">
        <template #extra>
          <span class="hint">请回到发起授权的应用重试；若反复出现请联系应用方检查注册信息。</span>
        </template>
      </el-result>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api, ssoAuthorizeGet, errMsg, assertSafeRedirect, type SsoAuthorizeDesc } from '../api'

// SSO 授权确认页（契约 §2）：三方应用引导用户到本页（携带 OAuth2 参数），
// 前端渲染确认界面，approve/deny 后顶层跳转 redirect_to（非 iframe）。
const route = useRoute()
const desc = ref<SsoAuthorizeDesc | null>(null)
const errorText = ref('')
const loading = ref(true)
const submitting = ref(false)

// hash 路由下 query 经 route.query 透传（/sso/authorize?response_type=...）
const oauthParams = new URLSearchParams()
for (const k of ['response_type', 'client_id', 'redirect_uri', 'scope', 'state', 'code_challenge', 'code_challenge_method']) {
  const v = route.query[k]
  if (typeof v === 'string' && v) oauthParams.set(k, v)
}

onMounted(async () => {
  try {
    // GET /authorize：校验通过返回授权描述；失败为 OAuth2 错误体（非 Envelope）
    desc.value = await ssoAuthorizeGet(oauthParams)
  } catch (e) {
    errorText.value = errMsg(e)
  } finally {
    loading.value = false
  }
})

async function decide(action: 'approve' | 'deny') {
  submitting.value = true
  try {
    const body: Record<string, string> = { action }
    oauthParams.forEach((v, k) => { body[k] = v })
    const d = await api<{ redirect_to: string }>('/api/v1/identity/sso/authorize', {
      method: 'POST',
      body,
    })
    // 契约 §2.2：前端职责为顶层跳转（deny 时 redirect_to 携带 error=access_denied）
    // 双重兜底：后端已校验，但前端二次做 scheme 白名单防 open redirect
    window.location.href = assertSafeRedirect(d.redirect_to)
  } catch (e) {
    errorText.value = errMsg(e)
    desc.value = null
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.authz-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--semi-grey-0);
}
.authz-card { width: 440px; border-radius: var(--semi-border-radius-large); }
.hint { color: var(--semi-color-text-2); font-size: 13px; line-height: 1.7; }
</style>

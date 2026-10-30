<template>
  <div class="cb-wrap">
    <el-card class="cb-card">
      <template #header>三方登录</template>
      <div v-if="doing" style="text-align: center; padding: 24px 0">
        <el-icon class="is-loading" :size="28"><Loading /></el-icon>
        <p class="hint">正在完成登录…</p>
      </div>
      <el-result v-else-if="ok" icon="success" title="登录成功" sub-title="正在进入控制台…">
        <template #extra><el-button type="primary" @click="goHome">立即进入</el-button></template>
      </el-result>
      <el-result v-else icon="error" title="三方登录失败" :sub-title="errorText">
        <template #extra>
          <el-button type="primary" @click="toLogin">返回登录</el-button>
        </template>
      </el-result>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Loading } from '@element-plus/icons-vue'
import { token } from '../api'

// 钉钉等三方登录回调页（契约 §4 修订版）：后端 callback 302 至本页。
// 成功携带 token（JWT）+expires_at（unix 秒）；失败携带 error+error_description。
// 说明：302 未回传 state，防伪由服务端 verifyState 承担（签发前校验），
// 前端无法二次比对（sessionStorage 预存值仅作遗留清理）。
const route = useRoute()
const router = useRouter()
const ok = ref(false)
const doing = ref(true)
const errorText = ref('')

onMounted(() => {
  sessionStorage.removeItem('basalt_sso_state')
  const q = route.query
  const t = typeof q.token === 'string' ? q.token : ''
  if (t) {
    token.set(t)
    ok.value = true
    doing.value = false
    setTimeout(goHome, 600)
  } else {
    const code = typeof q.error === 'string' ? q.error : 'unknown'
    const desc = typeof q.error_description === 'string' ? q.error_description : ''
    errorText.value = desc ? `${code}：${desc}` : `错误码 ${code}`
    doing.value = false
  }
})

function goHome() { router.push('/') }
function toLogin() { router.push('/login') }
</script>

<style scoped>
.cb-wrap { min-height: 100vh; display: flex; align-items: center; justify-content: center; background: var(--semi-grey-0); }
.cb-card { width: 400px; border-radius: var(--semi-border-radius-large); }
.hint { color: var(--semi-color-text-2); font-size: 13px; }
</style>

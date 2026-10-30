<template>
  <div class="login-wrap">
    <el-card class="login-card">
      <h2 style="margin: 0 0 4px">玄武 Basalt</h2>
      <p style="color: var(--semi-color-text-2); margin: 0 0 20px; font-size: 13px">纯开源 · AI 原生 · B/G 端后端平台</p>
      <el-form @submit.prevent="doLogin">
        <el-form-item>
          <el-input v-model="username" placeholder="用户名" size="large" autofocus />
        </el-form-item>
        <el-form-item>
          <el-input v-model="password" type="password" placeholder="密码" size="large" show-password @keyup.enter="doLogin" />
        </el-form-item>
        <el-button type="primary" size="large" style="width: 100%" :loading="loading" @click="doLogin">登 录</el-button>
      </el-form>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from '../element'
import { api, token, perms, errMsg, type MeInfo } from '../api'

const username = ref('admin')
const password = ref('')
const loading = ref(false)
const router = useRouter()

async function doLogin() {
  if (!username.value || !password.value) return
  loading.value = true
  try {
    const data = await api<{ token: string }>('/api/v1/identity/login', {
      method: 'POST',
      body: { username: username.value, password: password.value },
    })
    token.set(data.token)
    // 缓存权限点供路由守卫与按钮级控制（每次登录刷新）
    try {
      const me = await api<MeInfo>('/api/v1/identity/me')
      perms.set(me.permissions || [])
    } catch { /* 权限拉取失败不阻断登录 */ }
    router.push('/')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--semi-grey-0);
}
.login-card { width: 360px; border-radius: var(--semi-border-radius-large); }
</style>

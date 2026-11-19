<template>
  <div class="login-page">
    <!-- 左侧品牌区 -->
    <section class="brand-side ape-gradient">
      <div class="brand-deco deco-1" />
      <div class="brand-deco deco-2" />
      <div class="brand-deco deco-3" />

      <div class="brand-top">
        <div class="brand-mark">
          <div class="brand-logo">玄</div>
          <span>玄武 Basalt</span>
        </div>
        <h1>AI 原生的<br />后端管理平台</h1>
        <p class="brand-desc">RBAC 权限 · 审批工作流 · 审计链 · AI/Agent 控制台，单二进制交付。</p>
        <ul class="brand-points">
          <li><span class="dot" />多租户 RBAC 与细粒度权限点</li>
          <li><span class="dot" />可视化流程设计器与并行网关</li>
          <li><span class="dot" />AI 用量、Agent 编排与运行监控</li>
        </ul>
      </div>

      <div class="brand-foot">纯开源 · 可私有化部署 · Go + Vue3</div>
    </section>

    <!-- 右侧表单区 -->
    <section class="form-side">
      <div class="form-card">
        <div class="form-head">
          <h2>欢迎回来</h2>
          <p>登录玄武 Basalt 管理控制台</p>
        </div>

        <el-form @submit.prevent="doLogin">
          <el-form-item>
            <el-input v-model="username" placeholder="用户名" size="large" autofocus />
          </el-form-item>
          <el-form-item>
            <el-input
              v-model="password"
              type="password"
              placeholder="密码"
              size="large"
              show-password
              @keyup.enter="doLogin"
            />
          </el-form-item>
          <el-button type="primary" size="large" class="submit" :loading="loading" @click="doLogin">
            登 录
          </el-button>
          <el-button size="large" class="dingtalk" @click="dingtalkLogin">钉钉登录</el-button>
        </el-form>

        <div class="form-foot">继续即表示同意平台使用规范与隐私政策</div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from '../element'
import { api, token, perms, errMsg, assertSafeRedirect, type MeInfo } from '../api'

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

// 钉钉三方登录（契约 §4 修订版）：取 authorize-url → 顶层跳转；
// 回跳闭环由后端 callback 302 至 /#/sso/social/callback 完成。
// 未配置 BASALT_SSO_DINGTALK_* 时端点 404，按提示处理（按钮常显，
// 点击反馈"未配置"——避免预探测请求）。
async function dingtalkLogin() {
  try {
    const d = await api<{ url: string; state: string }>('/api/v1/identity/sso/social/dingtalk/authorize-url')
    // state 由服务端 HMAC 签名校验（防伪主防线）；预存一份供排障对照
    if (d.state) sessionStorage.setItem('basalt_sso_state', d.state)
    // 前端 scheme 白名单（防 open redirect，后端已校验但双重兜底）
    window.location.href = assertSafeRedirect(d.url)
  } catch (e) {
    ElMessage.error('钉钉登录不可用：' + errMsg(e))
  }
}
</script>

<style scoped>
.login-page {
  display: flex;
  min-height: 100vh;
  background: var(--ape-body-bg);
}

/* ===== 左侧品牌区 ===== */
.brand-side {
  position: relative;
  flex: 1.15;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  padding: 56px 64px;
  color: #fff;
  overflow: hidden;
}
.brand-deco {
  position: absolute;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.14);
  pointer-events: none;
}
.deco-1 {
  width: 320px;
  height: 320px;
  right: -90px;
  top: -80px;
}
.deco-2 {
  width: 180px;
  height: 180px;
  left: -60px;
  bottom: 120px;
  background: rgba(255, 255, 255, 0.1);
}
.deco-3 {
  width: 96px;
  height: 96px;
  right: 140px;
  bottom: 60px;
  background: rgba(255, 255, 255, 0.16);
}
.brand-top {
  position: relative;
  z-index: 1;
  max-width: 520px;
}
.brand-mark {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 17px;
  font-weight: 600;
  letter-spacing: 0.5px;
}
.brand-logo {
  width: 42px;
  height: 42px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 14px;
  background: rgba(255, 255, 255, 0.2);
  border: 1px solid rgba(255, 255, 255, 0.36);
  font-size: 20px;
  font-weight: 700;
}
.brand-side h1 {
  margin: 48px 0 18px;
  font-size: 40px;
  line-height: 1.25;
  font-weight: 700;
  letter-spacing: 1px;
}
.brand-desc {
  margin: 0 0 30px;
  font-size: 15px;
  line-height: 1.7;
  color: rgba(255, 255, 255, 0.88);
}
.brand-points {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.brand-points li {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
  color: rgba(255, 255, 255, 0.94);
}
.brand-points .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 0 0 4px rgba(255, 255, 255, 0.18);
}
.brand-foot {
  position: relative;
  z-index: 1;
  font-size: 12.5px;
  color: rgba(255, 255, 255, 0.72);
}

/* ===== 右侧表单区 ===== */
.form-side {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px 32px;
  background: var(--ape-card-bg);
}
.form-card {
  width: 100%;
  max-width: 368px;
}
.form-head h2 {
  margin: 0 0 8px;
  font-size: 26px;
  font-weight: 700;
  color: var(--ape-text);
}
.form-head p {
  margin: 0 0 28px;
  font-size: 13.5px;
  color: var(--ape-text-secondary);
}
.submit {
  width: 100%;
  margin-top: 6px;
  font-weight: 600;
  letter-spacing: 4px;
}
.dingtalk {
  width: 100%;
  margin: 12px 0 0;
}
.form-foot {
  margin-top: 26px;
  font-size: 12px;
  color: var(--ape-text-placeholder);
  text-align: center;
}

@media (max-width: 900px) {
  .brand-side {
    display: none;
  }
  .form-side {
    background: var(--ape-body-bg);
  }
  .form-card {
    padding: 30px 24px;
    background: var(--ape-card-bg);
    border-radius: var(--ape-radius-lg);
    box-shadow: var(--ape-shadow-card);
  }
}
</style>

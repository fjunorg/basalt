<template>
  <div class="page">
    <div class="toolbar">
      <el-button @click="load">刷新</el-button>
      <span class="hint">登录会话注册表（令牌指纹，不存原始 token）；踢下线对目标用户全部会话立即生效，重新登录即恢复</span>
    </div>
    <el-table :data="rows" border stripe size="small" v-loading="loading">
      <el-table-column prop="username" label="用户名" width="140" />
      <el-table-column prop="user_id" label="用户 ID" width="200" show-overflow-tooltip />
      <el-table-column prop="tenant_id" label="租户" width="130" show-overflow-tooltip>
        <template #default="{ row }">{{ row.tenant_id || '平台级' }}</template>
      </el-table-column>
      <el-table-column prop="remote" label="来源 IP" width="130" />
      <el-table-column prop="user_agent" label="User-Agent" min-width="200" show-overflow-tooltip />
      <el-table-column label="登录时间" width="165">
        <template #default="{ row }">{{ new Date(row.login_at).toLocaleString('zh-CN') }}</template>
      </el-table-column>
      <el-table-column label="过期时间" width="165">
        <template #default="{ row }">{{ new Date(row.expires_at).toLocaleString('zh-CN') }}</template>
      </el-table-column>
      <el-table-column label="操作" width="110" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('identity:session:manage')" size="small" type="danger" @click="kick(row)">踢下线</el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

// API 依据 pkg/modules/identity session.go/handler.go（platform-small-pack）
interface OnlineSession {
  token_id: string
  user_id: string
  username: string
  tenant_id?: string
  remote: string
  user_agent: string
  login_at: string
  expires_at: string
}

const can = (p: string) => hasPerm(perms.get(), p)
const rows = ref<OnlineSession[]>([])
const loading = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

async function load() {
  loading.value = true
  try {
    rows.value = await api<OnlineSession[]>('/api/v1/identity/sessions')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function kick(row: OnlineSession) {
  try {
    await ElMessageBox.confirm(`将「${row.username}」的全部会话踢下线？其后续请求立即 401，重新登录可恢复。`, '确认', { type: 'warning' })
    await api(`/api/v1/identity/sessions/user/${row.user_id}`, { method: 'DELETE' })
    ElMessage.success('已踢下线')
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000) // 30s 轮询在线状态
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; }
</style>

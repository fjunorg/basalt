<template>
  <el-container style="min-height: 100vh">
    <el-aside width="210px" style="background: #1f2d3d">
      <div class="brand">玄武 Basalt</div>
      <el-menu :default-active="$route.path" router background-color="#1f2d3d" text-color="#bfcbd9" active-text-color="#409EFF">
        <el-menu-item index="/overview"><el-icon><Odometer /></el-icon>概览</el-menu-item>

        <el-sub-menu index="sys">
          <template #title><el-icon><Setting /></el-icon>系统管理</template>
          <el-menu-item v-if="perm('identity:user:list')" index="/users">用户管理</el-menu-item>
          <el-menu-item v-if="perm('identity:role:manage')" index="/roles">角色管理</el-menu-item>
          <el-menu-item v-if="perm('org:dept:list') || perm('org:post:list')" index="/org">部门与岗位</el-menu-item>
          <el-menu-item v-if="perm('tenant:list')" index="/tenants">租户管理</el-menu-item>
          <el-menu-item v-if="perm('dict:type:list')" index="/dict">字典管理</el-menu-item>
          <el-menu-item v-if="perm('config:list')" index="/config">参数配置</el-menu-item>
          <el-menu-item v-if="perm('notice:list')" index="/notices">通知公告</el-menu-item>
          <el-menu-item v-if="perm('file:list')" index="/files">文件管理</el-menu-item>
          <el-menu-item v-if="perm('workflow:def:manage')" index="/wfdefs">流程定义</el-menu-item>
        </el-sub-menu>

        <el-sub-menu index="work">
          <template #title><el-icon><Briefcase /></el-icon>工作台</template>
          <el-menu-item index="/approvals">审批中心</el-menu-item>
          <el-menu-item index="/finance">财务中心</el-menu-item>
          <el-menu-item index="/documents">公文中心</el-menu-item>
          <el-menu-item index="/contracts">合同管理</el-menu-item>
          <el-menu-item index="/ai">AI 控制台</el-menu-item>
        </el-sub-menu>

        <el-sub-menu index="mon">
          <template #title><el-icon><Monitor /></el-icon>系统监控</template>
          <el-menu-item v-if="perm('audit:read')" index="/audit">审计链</el-menu-item>
          <el-menu-item v-if="perm('job:list')" index="/jobs">定时任务</el-menu-item>
        </el-sub-menu>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header class="topbar">
        <span style="font-weight: 600">{{ $route.meta.title || '' }}</span>
        <div style="flex: 1"></div>
        <span style="color: #909399; font-size: 13px; margin-right: 12px">{{ me?.username || '' }}</span>
        <el-button text type="danger" @click="logout">退出</el-button>
      </el-header>
      <el-main style="background: #f5f7fa">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Odometer, Setting, Briefcase, Monitor } from '@element-plus/icons-vue'
import { api, token, hasPerm } from '../api'

const me = ref(null)
const router = useRouter()

const perm = (p) => hasPerm(me.value?.permissions, p)

onMounted(async () => {
  try {
    me.value = await api('/api/v1/identity/me')
  } catch { /* 401 已由 api 层跳登录 */ }
})

function logout() {
  token.clear()
  localStorage.removeItem('basalt_perms')
  router.push('/login')
}
</script>

<style scoped>
.brand {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 18px;
  font-weight: 700;
  letter-spacing: 2px;
}
.topbar {
  display: flex;
  align-items: center;
  background: #fff;
  border-bottom: 1px solid #e4e7ed;
}
</style>

<template>
  <el-container style="min-height: 100vh">
    <el-aside width="220px" class="side-nav">
      <div class="brand">玄武 Basalt</div>
      <el-menu :default-active="$route.path" router>
        <!-- 动态模式：后端菜单树驱动（契约 menu-api-contract.md §2.1）；
             下发为空/拉取失败 → 回退下方静态块，行为与旧版完全一致 -->
        <MenuTree v-if="menuTree.length" :nodes="menuTree" />
        <!-- 菜单管理固定入口：不经动态树渲染（避免未播种时无入口管理菜单的
             死锁；可见性仅由 system:menu:list 控制，无循环依赖） -->
        <el-menu-item v-if="canRoute('/system/menus')" index="/system/menus">
          <el-icon><Menu /></el-icon>
          <template #title>菜单管理</template>
        </el-menu-item>
        <template v-if="!menuTree.length">
          <el-menu-item index="/overview"><el-icon><Odometer /></el-icon>概览</el-menu-item>

          <el-sub-menu index="sys">
            <template #title><el-icon><Setting /></el-icon>系统管理</template>
            <el-menu-item v-if="canRoute('/users')" index="/users">用户管理</el-menu-item>
            <el-menu-item v-if="canRoute('/roles')" index="/roles">角色管理</el-menu-item>
            <el-menu-item v-if="canRoute('/org')" index="/org">部门与岗位</el-menu-item>
            <el-menu-item v-if="canRoute('/tenants')" index="/tenants">租户管理</el-menu-item>
            <el-menu-item v-if="canRoute('/dict')" index="/dict">字典管理</el-menu-item>
            <el-menu-item v-if="canRoute('/config')" index="/config">参数配置</el-menu-item>
            <el-menu-item v-if="canRoute('/notices')" index="/notices">通知公告</el-menu-item>
            <el-menu-item v-if="canRoute('/files')" index="/files">文件管理</el-menu-item>
            <el-menu-item v-if="canRoute('/wfdefs')" index="/wfdefs">流程定义</el-menu-item>
            <el-menu-item v-if="canRoute('/notify')" index="/notify">通知中心</el-menu-item>
            <el-menu-item v-if="canRoute('/sso/apps')" index="/sso/apps">SSO 应用</el-menu-item>
            <el-menu-item v-if="canRoute('/tenant-pkgs')" index="/tenant-pkgs">租户套餐</el-menu-item>
            <el-menu-item v-if="canRoute('/errcodes')" index="/errcodes">错误码</el-menu-item>
          </el-sub-menu>

          <el-sub-menu index="work">
            <template #title><el-icon><Briefcase /></el-icon>工作台</template>
            <el-menu-item index="/approvals">审批中心</el-menu-item>
            <el-menu-item index="/finance">财务中心</el-menu-item>
            <el-menu-item index="/documents">公文中心</el-menu-item>
            <el-menu-item index="/contracts">合同管理</el-menu-item>
            <el-menu-item index="/ai">AI 控制台</el-menu-item>
            <el-menu-item v-if="canRoute('/agent-defs')" index="/agent-defs">Agent 管理</el-menu-item>
            <el-menu-item v-if="canRoute('/agent-run')" index="/agent-run">Agent 运行</el-menu-item>
          </el-sub-menu>

          <el-sub-menu index="mon">
            <template #title><el-icon><Monitor /></el-icon>系统监控</template>
            <el-menu-item v-if="canRoute('/audit')" index="/audit">审计链</el-menu-item>
            <el-menu-item v-if="canRoute('/jobs')" index="/jobs">定时任务</el-menu-item>
            <el-menu-item v-if="canRoute('/online-users')" index="/online-users">在线用户</el-menu-item>
            <el-menu-item v-if="canRoute('/op-logs')" index="/op-logs">系统日志</el-menu-item>
            <el-menu-item v-if="canRoute('/op-metrics')" index="/op-metrics">运行监控</el-menu-item>
          </el-sub-menu>
        </template>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header class="topbar">
        <span style="font-weight: 600">{{ $route.meta.title || '' }}</span>
        <div style="flex: 1"></div>
        <span style="color: var(--semi-color-text-2); font-size: 13px; margin-right: 12px">{{ me?.username || '' }}</span>
        <el-button text type="danger" @click="logout">退出</el-button>
      </el-header>
      <el-main style="background: var(--semi-grey-0)">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Odometer, Setting, Briefcase, Monitor, Menu } from '@element-plus/icons-vue'
import { api, token, perms, hasPerm, fetchMenusMe, type MeInfo, type MenuNode } from '../api'
import MenuTree from '../components/MenuTree.vue'

const me = ref<MeInfo | null>(null)
const router = useRouter()
// 后端下发菜单树（空数组 = 回退静态模式）
const menuTree = ref<MenuNode[]>([])

// 菜单显隐与路由守卫共用路由 meta.perms 这一份映射（main.ts 定义），
// 管理项按权限点显隐；工作台五页面向个人（auth-only）不做菜单级收敛。
const canRoute = (path: string) => {
  const need = router.resolve(path).meta?.perms || []
  return !need.length || need.some((p) => hasPerm(me.value?.permissions, p))
}

// 动态模式权限合成（契约 §4.4）：叶子节点 perm → 对应已注册路由的
// meta.perms（未注册 path 忽略不报错）；守卫逻辑不变，读同一 meta。
function applyDynamicPerms(nodes: MenuNode[]) {
  for (const n of nodes) {
    if (n.children?.length) {
      applyDynamicPerms(n.children)
    } else if (n.perm && n.path) {
      const rec = router.getRoutes().find((r) => r.path === n.path)
      if (rec) rec.meta.perms = [n.perm]
    }
  }
}

onMounted(async () => {
  try {
    me.value = await api<MeInfo>('/api/v1/identity/me')
    // 回写权限快照：角色变更后刷新页面即生效，保持守卫数据与后端一致
    perms.set(me.value?.permissions || [])
  } catch { /* 401 已由 api 层跳登录 */ }
  // 动态菜单：menus 非空 → 树驱动侧栏 + perm 合成；空/失败 → 静态回退
  const mm = await fetchMenusMe()
  if (mm && mm.menus.length) {
    menuTree.value = mm.menus
    // /menus/me 的 perms 快照与 /me 一致（契约 §2.1），回写保持守卫数据最新
    if (mm.perms?.length) perms.set(mm.perms)
    applyDynamicPerms(mm.menus)
  }
})

function logout() {
  token.clear()
  perms.clear()
  router.push('/login')
}
</script>

<style scoped>
.brand {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--semi-color-text-0);
  font-size: 18px;
  font-weight: 700;
  letter-spacing: 2px;
}
.topbar {
  display: flex;
  align-items: center;
  background: var(--semi-color-bg-0);
  border-bottom: 1px solid var(--semi-color-border);
}
</style>

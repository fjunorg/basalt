<template>
  <div class="layout">
    <!-- 侧边栏（移动端为抽屉） -->
    <aside class="sidebar" :class="{ collapsed, 'mobile-open': mobileOpen }">
      <div class="brand" @click="mobileOpen = false">
        <div class="brand-logo ape-gradient">玄</div>
        <div v-show="!collapsed" class="brand-text">
          <span class="brand-name">玄武 Basalt</span>
          <span class="brand-sub">AI 原生的后端平台</span>
        </div>
      </div>

      <el-scrollbar class="sidebar-scroll">
        <el-menu :default-active="$route.path" :collapse="collapsed" :collapse-transition="false" router>
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
            <el-menu-item index="/overview"><el-icon><Odometer /></el-icon><template #title>概览</template></el-menu-item>

            <el-sub-menu index="work">
              <template #title><el-icon><Briefcase /></el-icon><span>工作台</span></template>
              <el-menu-item index="/approvals">审批中心</el-menu-item>
              <el-menu-item index="/finance">财务中心</el-menu-item>
              <el-menu-item index="/documents">公文中心</el-menu-item>
              <el-menu-item index="/contracts">合同管理</el-menu-item>
              <el-menu-item index="/ai">AI 控制台</el-menu-item>
              <el-menu-item v-if="canRoute('/agent-defs')" index="/agent-defs">Agent 管理</el-menu-item>
              <el-menu-item v-if="canRoute('/agent-run')" index="/agent-run">Agent 运行</el-menu-item>
            </el-sub-menu>

            <el-sub-menu index="sys">
              <template #title><el-icon><Setting /></el-icon><span>系统管理</span></template>
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

            <el-sub-menu index="mon">
              <template #title><el-icon><Monitor /></el-icon><span>系统监控</span></template>
              <el-menu-item v-if="canRoute('/audit')" index="/audit">审计链</el-menu-item>
              <el-menu-item v-if="canRoute('/jobs')" index="/jobs">定时任务</el-menu-item>
              <el-menu-item v-if="canRoute('/online-users')" index="/online-users">在线用户</el-menu-item>
              <el-menu-item v-if="canRoute('/op-logs')" index="/op-logs">系统日志</el-menu-item>
              <el-menu-item v-if="canRoute('/op-metrics')" index="/op-metrics">运行监控</el-menu-item>
            </el-sub-menu>
          </template>
        </el-menu>
      </el-scrollbar>

      <div v-show="!collapsed" class="sidebar-footer">
        <div class="helper-card ape-gradient">
          <div class="helper-title">玄武助手</div>
          <div class="helper-desc">AI 控制台 · Agent 编排 · 一键体验</div>
          <button class="helper-btn" @click="router.push('/ai')">立即体验</button>
        </div>
      </div>
    </aside>

    <div v-if="mobileOpen" class="mask" @click="mobileOpen = false" />

    <!-- 主区 -->
    <div class="main-wrap">
      <header class="topbar">
        <el-button text class="icon-btn" @click="toggleSidebar">
          <el-icon :size="18"><Expand v-if="collapsed" /><Fold v-else /></el-icon>
        </el-button>

        <div class="page-crumb">
          <span class="crumb-root">玄武管理台</span>
          <span class="crumb-sep">/</span>
          <span class="crumb-current">{{ $route.meta.title || '概览' }}</span>
        </div>

        <div class="search-wrap">
          <div class="search-box">
            <el-icon class="search-icon" :size="16"><Search /></el-icon>
            <input
              v-model="keyword"
              class="search-input"
              placeholder="搜索页面，如：用户、角色、审批…"
              @focus="searchOpen = true"
              @blur="closeSearch"
              @keyup.enter="goFirst"
            />
            <div v-if="searchOpen && keyword.trim()" class="search-panel">
              <div v-if="!searchResults.length" class="search-empty">未找到匹配页面</div>
              <div
                v-for="r in searchResults"
                :key="r.path"
                class="search-item"
                @mousedown.prevent="go(r.path)"
              >
                <span class="search-item-title">{{ r.title }}</span>
                <span class="search-item-path">{{ r.path }}</span>
              </div>
            </div>
          </div>
        </div>

        <div class="topbar-right">
          <el-tooltip :content="dark ? '切换浅色' : '切换深色'" placement="bottom">
            <el-button text class="icon-btn" @click="toggleTheme">
              <el-icon :size="18"><Sunny v-if="dark" /><Moon v-else /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip content="全屏" placement="bottom">
            <el-button text class="icon-btn" @click="toggleFullscreen">
              <el-icon :size="18"><FullScreen /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip content="通知中心" placement="bottom">
            <el-button text class="icon-btn" @click="router.push('/notify')">
              <el-icon :size="18"><Bell /></el-icon>
            </el-button>
          </el-tooltip>

          <el-dropdown trigger="click" @command="onUserCommand">
            <div class="profile">
              <el-avatar :size="34" class="profile-avatar">{{ avatarText }}</el-avatar>
              <div class="profile-info">
                <span class="profile-name">{{ me?.username || '未登录' }}</span>
                <span class="profile-role">{{ roleText }}</span>
              </div>
              <el-icon :size="12" class="profile-arrow"><ArrowDown /></el-icon>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="overview">
                  <el-icon><Odometer /></el-icon>个人概览
                </el-dropdown-item>
                <el-dropdown-item command="logout" divided>
                  <el-icon><SwitchButton /></el-icon>退出登录
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </header>

      <el-main class="content">
        <router-view />
      </el-main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  Odometer, Setting, Briefcase, Monitor, Menu, Fold, Expand, Search,
  Moon, Sunny, FullScreen, Bell, ArrowDown, SwitchButton,
} from '@element-plus/icons-vue'
import { api, token, perms, hasPerm, fetchMenusMe, type MeInfo, type MenuNode } from '../api'
import MenuTree from '../components/MenuTree.vue'

const me = ref<MeInfo | null>(null)
const router = useRouter()
// 后端下发菜单树（空数组 = 回退静态模式）
const menuTree = ref<MenuNode[]>([])

const collapsed = ref(localStorage.getItem('basalt-sidebar') === 'collapsed')
const mobileOpen = ref(false)
const isMobile = ref(window.innerWidth < 900)
const dark = ref(document.documentElement.classList.contains('dark'))

const keyword = ref('')
const searchOpen = ref(false)

// 菜单显隐与路由守卫共用路由 meta.perms 这一份映射（main.ts 定义），
// 管理项按权限点显隐；工作台五页面向个人（auth-only）不做菜单级收敛。
const canRoute = (path: string) => {
  const need = router.resolve(path).meta?.perms || []
  return !need.length || need.some((p) => hasPerm(me.value?.permissions, p))
}

const avatarText = computed(() => (me.value?.username || 'U').slice(0, 1).toUpperCase())
const roleText = computed(() => ((me.value?.permissions?.length || 0) > 0 ? '平台用户' : '访客'))

// 全局页面搜索：按路由 meta.title 匹配，过滤无权限页面
const searchResults = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  if (!kw) return []
  const seen = new Set<string>()
  return router
    .getRoutes()
    .filter((r) => r.meta?.title && !r.path.includes(':') && canRoute(r.path))
    .filter((r) => {
      if (seen.has(r.path)) return false
      seen.add(r.path)
      return String(r.meta.title).toLowerCase().includes(kw)
    })
    .slice(0, 8)
    .map((r) => ({ path: r.path, title: String(r.meta.title) }))
})

function go(path: string) {
  keyword.value = ''
  searchOpen.value = false
  router.push(path)
  mobileOpen.value = false
}
const goFirst = () => { if (searchResults.value[0]) go(searchResults.value[0].path) }
const closeSearch = () => setTimeout(() => { searchOpen.value = false }, 120)

function toggleSidebar() {
  if (isMobile.value) {
    mobileOpen.value = !mobileOpen.value
    return
  }
  collapsed.value = !collapsed.value
  localStorage.setItem('basalt-sidebar', collapsed.value ? 'collapsed' : 'expanded')
}

function toggleTheme() {
  dark.value = !dark.value
  document.documentElement.classList.toggle('dark', dark.value)
  localStorage.setItem('basalt-theme', dark.value ? 'dark' : 'light')
}

function toggleFullscreen() {
  if (document.fullscreenElement) document.exitFullscreen()
  else document.documentElement.requestFullscreen?.()
}

function onUserCommand(cmd: string) {
  if (cmd === 'logout') logout()
  else if (cmd === 'overview') router.push('/overview')
}

const onResize = () => {
  isMobile.value = window.innerWidth < 900
  if (!isMobile.value) mobileOpen.value = false
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
  window.addEventListener('resize', onResize)
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

onBeforeUnmount(() => window.removeEventListener('resize', onResize))

function logout() {
  token.clear()
  perms.clear()
  router.push('/login')
}

// 路由切换时收起移动端抽屉
watch(() => router.currentRoute.value.fullPath, () => { mobileOpen.value = false })
</script>

<style scoped>
.layout {
  display: flex;
  min-height: 100vh;
  background: var(--ape-body-bg);
}

/* ===== 侧边栏 ===== */
.sidebar {
  position: sticky;
  top: 0;
  align-self: flex-start;
  display: flex;
  flex-direction: column;
  width: var(--ape-sidebar-w);
  height: 100vh;
  flex-shrink: 0;
  background: var(--ape-nav-bg);
  border-right: 1px solid var(--ape-border);
  transition: width 0.25s ease;
  z-index: 20;
}
.sidebar.collapsed {
  width: var(--ape-sidebar-collapsed-w);
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  height: var(--ape-header-h);
  padding: 0 18px;
  flex-shrink: 0;
  overflow: hidden;
  cursor: pointer;
}
.brand-logo {
  width: 38px;
  height: 38px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 12px;
  color: #fff;
  font-size: 19px;
  font-weight: 700;
  box-shadow: 0 6px 16px rgba(90, 103, 245, 0.32);
}
.brand-text {
  display: flex;
  flex-direction: column;
  line-height: 1.25;
  white-space: nowrap;
}
.brand-name {
  font-size: 16px;
  font-weight: 700;
  color: var(--ape-text);
}
.brand-sub {
  font-size: 11px;
  color: var(--ape-text-secondary);
}

.sidebar-scroll {
  flex: 1;
  min-height: 0;
}
.sidebar :deep(.el-menu) {
  border-right: none;
  background: transparent;
  padding: 4px 10px 12px;
}
.sidebar :deep(.el-menu--collapse) {
  width: auto;
}
.sidebar :deep(.el-menu-item),
.sidebar :deep(.el-sub-menu__title) {
  height: 42px;
  line-height: 42px;
  margin: 2px 0;
  border-radius: 10px;
  color: var(--ape-text-regular);
  font-size: 14px;
}
.sidebar :deep(.el-menu-item:hover),
.sidebar :deep(.el-sub-menu__title:hover) {
  background: var(--ape-primary-soft);
  color: var(--ape-primary);
}
.sidebar :deep(.el-menu-item.is-active) {
  background: var(--ape-primary);
  color: #fff;
  font-weight: 600;
  box-shadow: 0 6px 16px rgba(90, 103, 245, 0.28);
}
.sidebar :deep(.el-menu-item.is-active .el-icon) {
  color: #fff;
}
/* 二级菜单整体后缩并降低视觉权重 */
.sidebar :deep(.el-sub-menu .el-menu-item) {
  min-width: auto;
  height: 38px;
  line-height: 38px;
  padding-left: 48px !important;
  font-size: 13.5px;
}
.sidebar :deep(.el-menu--collapse .el-menu-item),
.sidebar :deep(.el-menu--collapse .el-sub-menu__title) {
  padding: 0 !important;
  display: flex;
  justify-content: center;
}

.sidebar-footer {
  padding: 12px;
  flex-shrink: 0;
}
.helper-card {
  position: relative;
  border-radius: 16px;
  padding: 16px;
  color: #fff;
  overflow: hidden;
  box-shadow: 0 10px 24px rgba(90, 103, 245, 0.28);
}
.helper-card::after {
  content: '';
  position: absolute;
  right: -22px;
  top: -22px;
  width: 84px;
  height: 84px;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.18);
}
.helper-title {
  font-size: 14px;
  font-weight: 700;
}
.helper-desc {
  margin: 6px 0 12px;
  font-size: 11.5px;
  opacity: 0.9;
  line-height: 1.5;
}
.helper-btn {
  position: relative;
  z-index: 1;
  width: 100%;
  border: none;
  border-radius: 8px;
  padding: 7px 0;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--ape-primary);
  background: #fff;
  cursor: pointer;
  transition: opacity 0.2s ease;
}
.helper-btn:hover {
  opacity: 0.88;
}

/* ===== 主区 ===== */
.main-wrap {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.topbar {
  position: sticky;
  top: 0;
  z-index: 15;
  display: flex;
  align-items: center;
  gap: 12px;
  height: var(--ape-header-h);
  padding: 0 20px;
  background: var(--ape-card-bg);
  border-bottom: 1px solid var(--ape-border);
}
.icon-btn {
  padding: 8px;
  color: var(--ape-text-regular);
}
.icon-btn:hover {
  background: var(--ape-primary-soft);
  color: var(--ape-primary);
}

.page-crumb {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  white-space: nowrap;
}
.crumb-root {
  color: var(--ape-text-secondary);
}
.crumb-sep {
  color: var(--ape-text-placeholder);
}
.crumb-current {
  font-weight: 600;
  color: var(--ape-text);
}

.search-wrap {
  flex: 1;
  display: flex;
  justify-content: flex-start;
  padding-left: 8px;
}
.search-box {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  width: min(380px, 100%);
  height: 38px;
  padding: 0 14px;
  border-radius: 10px;
  background: var(--ape-body-bg);
  border: 1px solid transparent;
  transition: border-color 0.2s ease, background 0.2s ease;
}
.search-box:focus-within {
  background: var(--ape-card-bg);
  border-color: var(--ape-primary);
}
.search-icon {
  color: var(--ape-text-secondary);
  flex-shrink: 0;
}
.search-input {
  flex: 1;
  min-width: 0;
  border: none;
  outline: none;
  background: transparent;
  font-size: 13px;
  color: var(--ape-text);
  font-family: var(--ape-font);
}
.search-input::placeholder {
  color: var(--ape-text-placeholder);
}
.search-panel {
  position: absolute;
  top: 46px;
  left: 0;
  right: 0;
  padding: 6px;
  background: var(--ape-card-bg);
  border: 1px solid var(--ape-border);
  border-radius: 12px;
  box-shadow: var(--ape-shadow-pop);
  z-index: 30;
}
.search-empty {
  padding: 10px 12px;
  font-size: 13px;
  color: var(--ape-text-secondary);
}
.search-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 9px 12px;
  border-radius: 8px;
  cursor: pointer;
}
.search-item:hover {
  background: var(--ape-primary-soft);
}
.search-item-title {
  font-size: 13.5px;
  color: var(--ape-text);
}
.search-item-path {
  font-size: 12px;
  color: var(--ape-text-secondary);
}

.topbar-right {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}
.profile {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-left: 8px;
  padding: 4px 10px 4px 4px;
  border-radius: 22px;
  cursor: pointer;
  transition: background 0.2s ease;
}
.profile:hover {
  background: var(--ape-primary-soft);
}
.profile-avatar {
  background: linear-gradient(135deg, var(--ape-grad-from), var(--ape-grad-to));
  color: #fff;
  font-size: 14px;
  font-weight: 600;
}
.profile-info {
  display: flex;
  flex-direction: column;
  line-height: 1.25;
}
.profile-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--ape-text);
  white-space: nowrap;
}
.profile-role {
  font-size: 11px;
  color: var(--ape-text-secondary);
  white-space: nowrap;
}
.profile-arrow {
  color: var(--ape-text-secondary);
}

.content {
  padding: 24px;
  background: var(--ape-body-bg);
}

/* ===== 移动端 ===== */
.mask {
  position: fixed;
  inset: 0;
  z-index: 19;
  background: rgba(10, 15, 35, 0.42);
  backdrop-filter: blur(2px);
}
@media (max-width: 900px) {
  .sidebar {
    position: fixed;
    left: 0;
    top: 0;
    bottom: 0;
    transform: translateX(-100%);
    box-shadow: var(--ape-shadow-pop);
    transition: transform 0.25s ease;
  }
  .sidebar.mobile-open {
    transform: translateX(0);
  }
  .sidebar.collapsed {
    width: var(--ape-sidebar-w);
  }
  .profile-info {
    display: none;
  }
  .page-crumb {
    display: none;
  }
  .content {
    padding: 16px;
  }
  .search-box {
    width: 100%;
  }
}
</style>

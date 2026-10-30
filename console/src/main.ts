import { createApp } from 'vue'
import { createRouter, createWebHashHistory, type RouteRecordRaw } from 'vue-router'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import App from './App.vue'
import { api, token, perms, hasPerm, type MeInfo } from './api'

// 页面级权限映射：meta.perms 列出进入该页面所需权限点（满足任一即可），
// 取值依据后端各模块路由挂载（pkg/modules/** 的 Require 中间件）：
// - 用户管理 GET /users → identity:user:list
// - 角色管理 GET /roles → identity:role:manage
// - 部门与岗位 GET /depts/tree、/posts → org:dept:list / org:post:list
// - 租户管理 GET /tenants → tenant:list
// - 字典管理 GET /types、/datas → dict:type:list
// - 参数配置 GET /api/v1/configs → config:list
// - 通知公告 GET /notices → notice:list
// - 文件管理 GET /files → file:list
// - 流程定义（含创建/编辑/删除）→ workflow:def:manage
// - 审计链 GET /audit/events、/verify → audit:read
// - 定时任务 GET /jobs → job:list
// 工作台五页（审批/财务/公文/合同/AI）主数据面向个人（auth-only，mine/todo 流），
// 不设页面级权限点；其中的管理型操作由页面内按钮级 can() 控制。
const routes: RouteRecordRaw[] = [
  { path: '/login', component: () => import('./views/Login.vue') },
  {
    path: '/',
    component: () => import('./views/Layout.vue'),
    redirect: '/overview',
    children: [
      { path: 'overview', component: () => import('./views/Overview.vue'), meta: { title: '概览' } },
      { path: 'users', component: () => import('./views/Users.vue'), meta: { title: '用户管理', perms: ['identity:user:list'] } },
      { path: 'org', component: () => import('./views/Org.vue'), meta: { title: '部门与岗位', perms: ['org:dept:list', 'org:post:list'] } },
      { path: 'dict', component: () => import('./views/Dict.vue'), meta: { title: '字典管理', perms: ['dict:type:list'] } },
      { path: 'config', component: () => import('./views/SysConfig.vue'), meta: { title: '参数配置', perms: ['config:list'] } },
      { path: 'notices', component: () => import('./views/Notices.vue'), meta: { title: '通知公告', perms: ['notice:list'] } },
      { path: 'files', component: () => import('./views/Files.vue'), meta: { title: '文件管理', perms: ['file:list'] } },
      { path: 'jobs', component: () => import('./views/Jobs.vue'), meta: { title: '定时任务', perms: ['job:list'] } },
      { path: 'wfdefs', component: () => import('./views/WfDefs.vue'), meta: { title: '流程定义', perms: ['workflow:def:manage'] } },
      { path: 'finance', component: () => import('./views/Finance.vue'), meta: { title: '财务中心' } },
      { path: 'documents', component: () => import('./views/Documents.vue'), meta: { title: '公文中心' } },
      { path: 'contracts', component: () => import('./views/Contracts.vue'), meta: { title: '合同管理' } },
      { path: 'roles', component: () => import('./views/Roles.vue'), meta: { title: '角色管理', perms: ['identity:role:manage'] } },
      { path: 'tenants', component: () => import('./views/Tenants.vue'), meta: { title: '租户管理', perms: ['tenant:list'] } },
      { path: 'approvals', component: () => import('./views/Approvals.vue'), meta: { title: '审批中心' } },
      { path: 'audit', component: () => import('./views/Audit.vue'), meta: { title: '审计链', perms: ['audit:read'] } },
      { path: 'ai', component: () => import('./views/AiConsole.vue'), meta: { title: 'AI 控制台' } },
      { path: 'forbidden', component: () => import('./views/Forbidden.vue'), meta: { title: '无权访问' } },
    ],
  },
]

const router = createRouter({ history: createWebHashHistory(), routes })

// 全局守卫：登录校验 + 页面级权限拦截（与 API 侧 Require 同规则）。
router.beforeEach(async (to) => {
  if (to.path === '/login') return true
  if (!token.get()) return '/login'
  const need = to.meta?.perms || []
  if (!need.length) return true
  let p = perms.get()
  if (p === null) {
    // 旧会话遗留 token 且无权限快照：拉取一次 /me 自愈（此后守卫与菜单共用同一快照）
    try {
      const me = await api<MeInfo>('/api/v1/identity/me')
      perms.set(me.permissions || [])
      p = perms.get() || []
    } catch {
      return true // 401 已由 api 层统一跳登录；其余失败放行，由页面层 API 403 兜底
    }
  }
  return need.some((w) => hasPerm(p, w))
    ? true
    : { path: '/forbidden', query: { to: to.fullPath, need: need.join(',') } }
})

createApp(App).use(router).use(ElementPlus, { locale: zhCn }).mount('#app')

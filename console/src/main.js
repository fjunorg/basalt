import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import App from './App.vue'
import { token } from './api'

const routes = [
  { path: '/login', component: () => import('./views/Login.vue') },
  {
    path: '/',
    component: () => import('./views/Layout.vue'),
    redirect: '/overview',
    children: [
      { path: 'overview', component: () => import('./views/Overview.vue'), meta: { title: '概览' } },
      { path: 'users', component: () => import('./views/Users.vue'), meta: { title: '用户管理' } },
      { path: 'org', component: () => import('./views/Org.vue'), meta: { title: '部门与岗位' } },
      { path: 'dict', component: () => import('./views/Dict.vue'), meta: { title: '字典管理' } },
      { path: 'config', component: () => import('./views/SysConfig.vue'), meta: { title: '参数配置' } },
      { path: 'notices', component: () => import('./views/Notices.vue'), meta: { title: '通知公告' } },
      { path: 'files', component: () => import('./views/Files.vue'), meta: { title: '文件管理' } },
      { path: 'jobs', component: () => import('./views/Jobs.vue'), meta: { title: '定时任务' } },
      { path: 'wfdefs', component: () => import('./views/WfDefs.vue'), meta: { title: '流程定义' } },
      { path: 'finance', component: () => import('./views/Finance.vue'), meta: { title: '财务中心' } },
      { path: 'documents', component: () => import('./views/Documents.vue'), meta: { title: '公文中心' } },
      { path: 'contracts', component: () => import('./views/Contracts.vue'), meta: { title: '合同管理' } },
      { path: 'roles', component: () => import('./views/Roles.vue'), meta: { title: '角色管理' } },
      { path: 'tenants', component: () => import('./views/Tenants.vue'), meta: { title: '租户管理' } },
      { path: 'approvals', component: () => import('./views/Approvals.vue'), meta: { title: '审批中心' } },
      { path: 'audit', component: () => import('./views/Audit.vue'), meta: { title: '审计链' } },
      { path: 'ai', component: () => import('./views/AiConsole.vue'), meta: { title: 'AI 控制台' } },
    ],
  },
]

const router = createRouter({ history: createWebHashHistory(), routes })
router.beforeEach((to) => {
  if (to.path !== '/login' && !token.get()) return '/login'
})

createApp(App).use(router).use(ElementPlus, { locale: zhCn }).mount('#app')

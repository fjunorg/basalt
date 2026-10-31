// 统一 API 封装：Bearer 注入、Envelope 解包、401 跳登录。
export const token = {
  get: (): string => localStorage.getItem('basalt_token') || '',
  set: (t: string) => localStorage.setItem('basalt_token', t),
  clear: () => localStorage.removeItem('basalt_token'),
}

// 权限点快照：登录与用户信息刷新时写入，路由守卫与侧边菜单共用同一数据源。
// get 返回 null 表示从未写入（区别于空数组 = 确认无任何权限点）。
export const perms = {
  get: (): string[] | null => {
    try {
      const raw = localStorage.getItem('basalt_perms')
      return raw === null ? null : (JSON.parse(raw) as string[])
    } catch {
      return null
    }
  },
  set: (p: string[]) => localStorage.setItem('basalt_perms', JSON.stringify(p || [])),
  clear: () => localStorage.removeItem('basalt_perms'),
}

// 后端统一响应信封（web.OK / web.Err）。
export interface Envelope<T = unknown> {
  code: number
  message: string
  data: T
}

// GET /api/v1/identity/me 响应：用户字段 + 汇总后的权限点集合
// （identity handler.go meResp；字段以实际返回为准，仅声明前端消费部分）。
export interface MeInfo {
  id: string
  username: string
  permissions: string[]
}

export interface ApiOptions {
  method?: string
  body?: unknown
}

export async function api<T = unknown>(path: string, { method = 'GET', body }: ApiOptions = {}): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...(token.get() ? { Authorization: 'Bearer ' + token.get() } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 401) {
    token.clear()
    location.hash = '#/login'
    throw new Error('未登录或会话过期')
  }
  const env = (await res.json().catch(() => ({ code: res.status, message: '响应解析失败' }))) as Envelope<T>
  if (!res.ok || env.code !== 0) {
    throw new Error(env.message || `请求失败(${res.status})`)
  }
  return env.data
}

// catch 分支错误信息提取（strict 模式 catch 变量为 unknown）。
export const errMsg = (e: unknown): string => (e instanceof Error ? e.message : String(e))

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system'
  content: string
}

export async function chatStream({ model, messages, onDelta }: {
  model: string
  messages: ChatMessage[]
  onDelta: (delta: string) => void
}): Promise<void> {
  const res = await fetch('/api/v1/ai/chat/completions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: 'Bearer ' + token.get(),
    },
    body: JSON.stringify({ model, messages, stream: true }),
  })
  if (!res.ok) {
    const env = (await res.json().catch(() => ({}))) as Partial<Envelope>
    throw new Error(env.message || `请求失败(${res.status})`)
  }
  const reader = res.body!.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    const lines = buf.split('\n')
    buf = lines.pop() || ''
    for (const line of lines) {
      if (!line.startsWith('data: ')) continue
      const data = line.slice(6)
      if (data === '[DONE]') return
      try {
        const chunk = JSON.parse(data) as { choices?: Array<{ delta?: { content?: string } }> }
        const delta = chunk.choices?.[0]?.delta?.content
        if (delta) onDelta(delta)
      } catch { /* 忽略非 JSON 心跳行 */ }
    }
  }
}

// hasPerm 前端权限匹配（与后端 identity.HasPermission 同规则）：
// "*" 全量、前缀通配（"identity:*"）、精确匹配。
export function hasPerm(perms: string[] | null | undefined, want: string): boolean {
  return (perms || []).some((p) => p === '*' || p === want || (p.endsWith('*') && want.startsWith(p.slice(0, -1))))
}

// ===== OpLog 操作日志与运行时指标（契约 oplog-api-contract.md）=====

export interface OpLogRow {
  id: string
  time: string
  method: string
  path: string
  status: number
  duration_ms: number
  user_id?: string
  remote?: string
  user_agent?: string
  module?: string
}

export interface OpLogPage {
  rows: OpLogRow[]
  total: number
  page: number
  page_size: number
}

export interface OpMetrics {
  window: { window_seconds: number; requests: number; qps: number; error_rate: number; p50_ms: number; p95_ms: number; p99_ms: number }
  process: { goroutines: number; heap_alloc_mb: number; sys_mb: number; num_gc: number; go_version: string }
  collector: { dropped: number; buffered: number; mode: string }
}

// xlsx 导出下载（Bearer 携带 + blob 落盘；导出端点均为 GET 返回
// application/vnd.openxmlformats-officedocument.spreadsheetml.sheet）
export async function downloadXlsx(path: string, filename: string): Promise<void> {
  const res = await fetch(path, {
    headers: { Authorization: 'Bearer ' + token.get() },
  })
  if (!res.ok) {
    const env = (await res.json().catch(() => ({}))) as Partial<Envelope>
    throw new Error(env.message || `导出失败(${res.status})`)
  }
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

// 用户导入结果（identity user_io.go：{imported, failed, errors}）
export interface ImportResult {
  imported: number
  failed: number
  errors: Array<{ row: number; reason: string }>
}

// ===== SSO/OAuth2（契约 sso-api-contract.md）=====

// §1 应用实体（Client；secret 仅创建/轮换响应一次性出现）
export interface SsoApp {
  id: string
  client_id: string
  name: string
  redirect_uris?: string[]
  scopes?: string[]
  grants?: string[]
  is_public: boolean
  status: string
  tenant_id?: string
  created_at?: string
  secret?: string
}

// §2.1 GET /authorize 授权描述（确认页渲染）
export interface SsoAuthorizeDesc {
  client_name: string
  scopes?: string[]
  redirect_uri: string
  state?: string
}

// §2 授权端点专用请求：错误体为 RFC 6749 格式（非 Envelope），
// 不走通用 api() 解包，独立 fetch 处理两种响应形态。
export async function ssoAuthorizeGet(params: URLSearchParams): Promise<SsoAuthorizeDesc> {
  const res = await fetch('/api/v1/identity/sso/authorize?' + params, {
    headers: { Authorization: 'Bearer ' + token.get() },
  })
  const body = (await res.json().catch(() => ({}))) as Record<string, unknown>
  if (!res.ok) {
    // OAuth2 错误体：{error, error_description}（契约头部约定）
    const err = body.error as string | undefined
    const desc = body.error_description as string | undefined
    throw new Error(desc || err || `请求失败(${res.status})`)
  }
  return (body.data as SsoAuthorizeDesc) || (body as unknown as SsoAuthorizeDesc)
}

// ===== 菜单模块（契约 menu-api-contract.md v1）=====

// §1 MenuNode（children 仅 /me 树返回）
export interface MenuNode {
  id: string
  parent_id: string
  name: string
  path: string
  icon?: string
  perm?: string
  sort: number
  enabled: boolean
  children?: MenuNode[]
}

// §2.1 GET /api/v1/menus/me 响应
export interface MenusMe {
  menus: MenuNode[]
  perms: string[]
  source: string
}

// §2.2 GET /api/v1/menus 全量列表项（扁平，含 role_count）
export interface MenuRow extends Omit<MenuNode, 'children'> {
  role_count?: number
}

export async function fetchMenusMe(): Promise<MenusMe | null> {
  // null = 拉取失败（网络/404 模块未装配）→ 调用方回退静态菜单
  return await api<MenusMe>('/api/v1/menus/me').catch(() => null)
}

// ===== Agent 模块（契约 agent-api-contract.md v1）=====

// §2.5 Definition
export interface AgentDef {
  id: string
  name: string
  description?: string
  system_prompt?: string
  model: string
  tools?: string[]
  temperature?: number
  max_tool_rounds?: number
  status: string
  owner_id?: string
  created_at?: string
  updated_at?: string
}

// §5 工具注册表项
export interface AgentTool {
  name: string
  description?: string
  parameters?: Record<string, unknown>
}

// §3.4 Run
export interface AgentRun {
  id: string
  agent_id: string
  user_id?: string
  status: 'running' | 'completed' | 'failed' | 'safety_rejected'
  input: string
  output?: string
  rounds?: number
  total_tokens?: number
  error?: string
  started_at?: string
  finished_at?: string
}

// §3.4 RunMessage
export interface AgentRunMessage {
  seq: number
  run_id?: string
  role: 'user' | 'assistant' | 'tool'
  content: string
  tool_calls?: Array<{ call_id: string; name: string; arguments: string }>
  tool_call_id?: string
  tool_name?: string
  created_at?: string
}

// §4 SSE 事件（event 名 + 已解析 data）
export interface AgentSseEvent {
  event: 'run.started' | 'tool.call' | 'tool.result' | 'llm.delta' | 'run.completed' | 'run.failed' | 'safety.rejected'
  data: {
    run_id?: string
    agent_id?: string
    model?: string
    call_id?: string
    name?: string
    arguments?: string
    ok?: boolean
    summary?: string
    result?: string
    text?: string
    status?: string
    rounds?: number
    total_tokens?: number
    output?: string
    error?: string
    side?: string
    message?: string
  }
}

// §3.1 发起运行（stream=true）。SSE 握手阶段（连接建立前）错误为普通
// JSON 信封（401/403/404/409 等），此处解析后抛 Error；进入事件流后
// 错误一律走 run.failed / safety.rejected 事件，由 onEvent 消费。
export async function agentRunStream(agentId: string, input: string, onEvent: (ev: AgentSseEvent) => void): Promise<void> {
  const res = await fetch(`/api/v1/agent/agents/${agentId}/runs`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: 'Bearer ' + token.get(),
    },
    body: JSON.stringify({ input, stream: true }),
  })
  if (!res.ok || !(res.headers.get('content-type') || '').includes('event-stream')) {
    const env = (await res.json().catch(() => ({}))) as Partial<Envelope>
    throw new Error(env.message || `请求失败(${res.status})`)
  }
  const reader = res.body!.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  let eventName = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    const lines = buf.split('\n')
    buf = lines.pop() || ''
    for (const line of lines) {
      if (line.startsWith('event: ')) {
        eventName = line.slice(7).trim()
      } else if (line.startsWith('data: ')) {
        const payload = line.slice(6)
        if (payload === '[DONE]') return
        try {
          onEvent({ event: eventName as AgentSseEvent['event'], data: JSON.parse(payload) })
        } catch { /* 忽略非 JSON 心跳行 */ }
      }
    }
  }
}

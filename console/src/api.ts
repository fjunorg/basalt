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

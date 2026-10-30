// 统一 API 封装：Bearer 注入、Envelope 解包、401 跳登录。
export const token = {
  get: () => localStorage.getItem('basalt_token') || '',
  set: (t) => localStorage.setItem('basalt_token', t),
  clear: () => localStorage.removeItem('basalt_token'),
}

export async function api(path, { method = 'GET', body } = {}) {
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
  const env = await res.json().catch(() => ({ code: res.status, message: '响应解析失败' }))
  if (!res.ok || env.code !== 0) {
    throw new Error(env.message || `请求失败(${res.status})`)
  }
  return env.data
}

export async function chatStream({ model, messages, onDelta }) {
  const res = await fetch('/api/v1/ai/chat/completions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: 'Bearer ' + token.get(),
    },
    body: JSON.stringify({ model, messages, stream: true }),
  })
  if (!res.ok) {
    const env = await res.json().catch(() => ({}))
    throw new Error(env.message || `请求失败(${res.status})`)
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    const lines = buf.split('\n')
    buf = lines.pop()
    for (const line of lines) {
      if (!line.startsWith('data: ')) continue
      const data = line.slice(6)
      if (data === '[DONE]') return
      try {
        const chunk = JSON.parse(data)
        const delta = chunk.choices?.[0]?.delta?.content
        if (delta) onDelta(delta)
      } catch { /* 忽略非 JSON 心跳行 */ }
    }
  }
}

// hasPerm 前端权限匹配（与后端 identity.HasPermission 同规则）：
// "*" 全量、前缀通配（"identity:*"）、精确匹配。
export function hasPerm(perms, want) {
  return (perms || []).some((p) => p === '*' || p === want || (p.endsWith('*') && want.startsWith(p.slice(0, -1))))
}

<template>
  <div class="page">
    <div class="toolbar">
      <el-button v-if="can('identity:sso:manage')" type="primary" @click="openCreate">新建应用</el-button>
      <el-button @click="load">刷新</el-button>
      <span class="hint">secret 仅创建/轮换时一次性下发（库中只存哈希）；redirect_uri 与注册值逐字节精确匹配</span>
    </div>
    <el-table :data="rows" border stripe size="small" v-loading="loading">
      <el-table-column prop="name" label="应用名" width="150" />
      <el-table-column prop="client_id" label="Client ID" width="200" show-overflow-tooltip />
      <el-table-column label="授权类型" width="180">
        <template #default="{ row }">
          <el-tag v-for="g in row.grants || []" :key="g" size="small" style="margin: 2px">{{ g }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Scope" min-width="130">
        <template #default="{ row }">
          <el-tag v-for="s in row.scopes || []" :key="s" size="small" type="success" style="margin: 2px">{{ s }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="客户端" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.is_public ? 'warning' : 'primary'">{{ row.is_public ? '公共(PKCE)' : '机密' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="回调 URI" min-width="200" show-overflow-tooltip>
        <template #default="{ row }">{{ (row.redirect_uris || []).join('；') || '-' }}</template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.status === 'active' ? 'success' : 'info'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="230" fixed="right">
        <template #default="{ row }">
          <el-button v-if="can('identity:sso:manage')" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button v-if="can('identity:sso:manage') && !row.is_public" size="small" type="warning" @click="rotate(row)">轮换</el-button>
          <el-button v-if="can('identity:sso:manage')" size="small" type="danger" @click="del(row)">删</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dlg" :title="editing ? '编辑应用' : '新建应用'" width="600">
      <el-form label-width="100">
        <el-form-item label="应用名" required><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="授权类型" required>
          <el-select v-model="form.grants" multiple style="width: 100%">
            <el-option label="authorization_code（授权码）" value="authorization_code" />
            <el-option label="client_credentials（客户端凭证）" value="client_credentials" />
          </el-select>
        </el-form-item>
        <el-form-item label="回调 URI">
          <el-input v-model="form.redirectUrisText" type="textarea" :rows="2"
            placeholder="每行一个绝对 URI（无 fragment）；授权码流程必填，与注册值逐字节精确匹配" />
        </el-form-item>
        <el-form-item label="Scopes">
          <el-input v-model="form.scopesText" placeholder="逗号分隔，如 profile, email" />
        </el-form-item>
        <el-form-item label="公共客户端">
          <el-switch v-model="form.is_public" />
          <div class="hint">SPA/移动端选公共客户端：无 secret，授权码流程强制 PKCE（S256）</div>
        </el-form-item>
        <el-form-item v-if="editing" label="状态">
          <el-radio-group v-model="form.status">
            <el-radio value="active">启用</el-radio>
            <el-radio value="disabled">停用</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <!-- secret 一次性下发对话框（创建/轮换共用） -->
    <el-dialog v-model="secretDlg" title="Secret 一次性下发" width="520">
      <el-alert type="warning" :closable="false" show-icon
        title="此 Secret 仅本次展示，关闭后不可再查（服务端只存哈希）。请立即复制并妥善保管。" />
      <div class="secret-box"><code>{{ secretValue }}</code></div>
      <template #footer>
        <el-button @click="copySecret">复制</el-button>
        <el-button type="primary" @click="secretDlg = false">我已保存，关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg, type SsoApp } from '../api'

// 页面级 identity:sso:manage（sso-api-contract.md §1，管理与页面同权限点）
const can = (p: string) => hasPerm(perms.get(), p)

const rows = ref<SsoApp[]>([])
const loading = ref(false)
const dlg = ref(false)
const saving = ref(false)
const editing = ref<SsoApp | null>(null)
const secretDlg = ref(false)
const secretValue = ref('')
const form = reactive({
  name: '',
  grants: [] as string[],
  redirectUrisText: '',
  scopesText: '',
  is_public: false,
  status: 'active',
})

async function load() {
  loading.value = true
  try {
    rows.value = await api<SsoApp[]>('/api/v1/identity/sso/apps')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

const parseLines = (t: string) => t.split(/[\n，;]+/).map((s) => s.trim()).filter(Boolean)
const parseComma = (t: string) => t.split(/[，,]/).map((s) => s.trim()).filter(Boolean)

function openCreate() {
  editing.value = null
  Object.assign(form, { name: '', grants: ['authorization_code'], redirectUrisText: '', scopesText: '', is_public: false, status: 'active' })
  dlg.value = true
}

function openEdit(row: SsoApp) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    grants: [...(row.grants || [])],
    redirectUrisText: (row.redirect_uris || []).join('\n'),
    scopesText: (row.scopes || []).join(', '),
    is_public: row.is_public,
    status: row.status,
  })
  dlg.value = true
}

async function save() {
  if (!form.name || !form.grants.length) {
    ElMessage.warning('应用名与授权类型必填')
    return
  }
  const uris = parseLines(form.redirectUrisText)
  if (form.grants.includes('authorization_code') && !uris.length) {
    ElMessage.warning('授权码流程必须至少注册一个回调 URI')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      // PATCH 可变字段（契约 §1：name/redirect_uris/scopes/grants/status）
      await api(`/api/v1/identity/sso/apps/${editing.value.id}`, {
        method: 'PATCH',
        body: {
          name: form.name,
          redirect_uris: uris,
          scopes: parseComma(form.scopesText),
          grants: form.grants,
          status: form.status,
        },
      })
      ElMessage.success('已保存')
    } else {
      const created = await api<SsoApp>('/api/v1/identity/sso/apps', {
        method: 'POST',
        body: {
          name: form.name,
          redirect_uris: uris,
          scopes: parseComma(form.scopesText),
          grants: form.grants,
          is_public: form.is_public,
        },
      })
      ElMessage.success('已创建')
      // 机密客户端：一次性展示 secret
      if (created.secret) {
        secretValue.value = created.secret
        secretDlg.value = true
      }
    }
    dlg.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function rotate(row: SsoApp) {
  try {
    await ElMessageBox.confirm(`轮换「${row.name}」的 secret？旧 secret 立即失效。`, '确认', { type: 'warning' })
    const d = await api<SsoApp>(`/api/v1/identity/sso/apps/${row.id}/secret`, { method: 'POST' })
    secretValue.value = d.secret || ''
    secretDlg.value = true
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

async function del(row: SsoApp) {
  try {
    await ElMessageBox.confirm(`删除应用「${row.name}」？其签发的 refresh token 链将失效。`, '确认', { type: 'warning' })
    await api(`/api/v1/identity/sso/apps/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

async function copySecret() {
  try {
    await navigator.clipboard.writeText(secretValue.value)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
}

onMounted(load)
</script>

<style scoped>
.hint { color: var(--semi-color-text-2); font-size: 12px; line-height: 1.6; }
.secret-box {
  margin-top: 12px;
  padding: 12px;
  background: var(--semi-grey-0);
  border-radius: var(--semi-border-radius-small);
  word-break: break-all;
  font-size: 13px;
}
</style>

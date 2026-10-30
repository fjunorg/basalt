<template>
  <div class="page">
    <el-tabs v-model="tab">
      <el-tab-pane label="我的报销" name="expense">
        <div class="toolbar">
          <el-button type="primary" @click="dlg = true">新建报销单</el-button>
          <el-button @click="loadExpenses">刷新</el-button>
        </div>
        <el-table :data="expenses" border stripe size="small" v-loading="loading">
          <el-table-column prop="business_key" label="单号" width="220" show-overflow-tooltip />
          <el-table-column prop="reason" label="事由" min-width="140" />
          <el-table-column label="金额" width="110">
            <template #default="{ row }">¥{{ (row.amount / 100).toFixed(2) }}</template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag size="small" :type="statusType(row.status)">{{ statusLabel(row.status) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="created_at" label="创建时间" width="170">
            <template #default="{ row }">{{ new Date(row.created_at).toLocaleString('zh-CN') }}</template>
          </el-table-column>
          <el-table-column label="操作" width="180" fixed="right">
            <template #default="{ row }">
              <el-button v-if="row.status === 'draft'" size="small" type="success" @click="submit(row)">提交审批</el-button>
              <el-button v-if="row.status === 'approved' && can('finance:payment:manage')" size="small" type="primary"
                @click="pay(row)">付款</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="发票登记" name="invoice" :disabled="!can('finance:invoice:manage')">
        <div class="toolbar">
          <el-button type="primary" @click="invDlg = true">登记发票</el-button>
          <el-button @click="loadInvoices">刷新</el-button>
        </div>
        <el-table :data="invoices" border stripe size="small">
          <el-table-column prop="code" label="发票号" width="150" />
          <el-table-column prop="title" label="抬头" min-width="180" />
          <el-table-column prop="tax_no" label="税号" width="170" />
          <el-table-column label="金额" width="110">
            <template #default="{ row }">¥{{ (row.amount / 100).toFixed(2) }}</template>
          </el-table-column>
          <el-table-column prop="date" label="开票日期" width="120">
            <template #default="{ row }">{{ new Date(row.date).toLocaleDateString('zh-CN') }}</template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="部门预算" name="budget" :disabled="!can('finance:budget:manage')">
        <div class="toolbar">
          <el-select v-model="budgetDept" placeholder="选择部门" style="width: 220px">
            <el-option v-for="d in deptFlat" :key="d.id" :value="d.id" :label="d.name" />
          </el-select>
          <el-input-number v-model="budgetLimit" :min="-1" :step="100000" placeholder="限额（分，-1 不限）" style="width: 220px" />
          <el-button type="primary" @click="saveBudget">设置本月预算</el-button>
          <el-button @click="loadBudgets">刷新</el-button>
        </div>
        <el-table :data="budgets" border stripe size="small">
          <el-table-column prop="dept_id" label="部门" width="200" show-overflow-tooltip>
            <template #default="{ row }">{{ deptName(row.dept_id) }}</template>
          </el-table-column>
          <el-table-column prop="period" label="账期" width="100" />
          <el-table-column label="限额" width="120">
            <template #default="{ row }">{{ row.limit < 0 ? '不限' : '¥' + (row.limit / 100).toFixed(2) }}</template>
          </el-table-column>
          <el-table-column label="已用" width="120">
            <template #default="{ row }">¥{{ (row.used / 100).toFixed(2) }}</template>
          </el-table-column>
          <el-table-column label="剩余" min-width="140">
            <template #default="{ row }">
              <el-progress :percentage="row.limit < 0 ? 0 : Math.min(100, Math.round(row.used / Math.max(row.limit, 1) * 100))"
                :status="row.limit >= 0 && row.used > row.limit ? 'exception' : undefined" />
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="付款记录" name="payment" :disabled="!can('finance:payment:manage')">
        <div class="toolbar"><el-button @click="loadPayments">刷新</el-button></div>
        <el-table :data="payments" border stripe size="small">
          <el-table-column prop="id" label="付款单" width="240" show-overflow-tooltip />
          <el-table-column prop="payee" label="收款方" min-width="140" />
          <el-table-column label="金额" width="120">
            <template #default="{ row }">¥{{ (row.amount / 100).toFixed(2) }}</template>
          </el-table-column>
          <el-table-column prop="status" label="状态" width="90" />
          <el-table-column prop="created_at" label="时间" width="170">
            <template #default="{ row }">{{ new Date(row.created_at).toLocaleString('zh-CN') }}</template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="dlg" title="新建报销单" width="560">
      <el-form label-width="80">
        <el-form-item label="部门">
          <el-select v-model="form.dept_id" clearable placeholder="选择部门（预算按部门管控）" style="width: 100%">
            <el-option v-for="d in deptFlat" :key="d.id" :value="d.id" :label="d.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="事由"><el-input v-model="form.reason" /></el-form-item>
      </el-form>
      <el-divider content-position="left">明细（金额单位：元）</el-divider>
      <div v-for="(it, i) in form.items" :key="i" style="display: flex; gap: 8px; margin-bottom: 8px">
        <el-input v-model="it.name" placeholder="项目" style="width: 160" />
        <el-input-number v-model="it.yuan" :min="0.01" :precision="2" :step="10" style="flex: 1" />
        <el-button text type="danger" @click="form.items.splice(i, 1)">删</el-button>
      </div>
      <el-button size="small" @click="form.items.push({ name: '', yuan: 10 })">+ 添加明细</el-button>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" @click="save">保存草稿</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="invDlg" title="登记发票" width="440">
      <el-form label-width="80">
        <el-form-item label="发票号"><el-input v-model="invForm.code" /></el-form-item>
        <el-form-item label="抬头"><el-input v-model="invForm.title" /></el-form-item>
        <el-form-item label="税号"><el-input v-model="invForm.tax_no" /></el-form-item>
        <el-form-item label="金额(分)"><el-input-number v-model="invForm.amount" :min="1" :step="1000" /></el-form-item>
        <el-form-item label="开票日期"><el-input v-model="invForm.date" placeholder="YYYY-MM-DD" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="invDlg = false">取消</el-button>
        <el-button type="primary" @click="saveInvoice">登记</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api, perms, hasPerm, errMsg } from '../api'

interface ExpenseRow { id: string; business_key: string; reason: string; amount: number; status: string; created_at: string }
interface InvoiceRow { id: string; code: string; title: string; tax_no: string; amount: number; date: string }
interface BudgetRow { dept_id: string; period: string; limit: number; used: number }
interface PaymentRow { id: string; payee: string; amount: number; status: string; created_at: string }
interface DeptRow { id: string; name: string; children?: DeptRow[] }

const can = (p: string) => hasPerm(perms.get(), p)

const tab = ref('expense')
const expenses = ref<ExpenseRow[]>([])
const invoices = ref<InvoiceRow[]>([])
const budgets = ref<BudgetRow[]>([])
const payments = ref<PaymentRow[]>([])
const deptFlat = ref<DeptRow[]>([])
const loading = ref(false)
const dlg = ref(false)
const invDlg = ref(false)
const budgetDept = ref('')
const budgetLimit = ref(1000000)
const form = reactive({ dept_id: '', reason: '', items: [{ name: '', yuan: 10 }] })
const invForm = reactive({ code: '', title: '', tax_no: '', amount: 1000, date: '' })

const statusLabel = (s: string) => ({ draft: '草稿', submitted: '审批中', approved: '已批', rejected: '被驳回', paid: '已付款' }[s] || s)
const statusType = (s: string) => ({ draft: 'info', submitted: 'warning', approved: 'success', rejected: 'danger', paid: 'primary' }[s] || 'info')
const deptName = (id: string) => deptFlat.value.find((d) => d.id === id)?.name || id

const flatten = (nodes: DeptRow[] | undefined, out: DeptRow[]): DeptRow[] => {
  for (const n of nodes || []) { out.push(n); flatten(n.children, out) }
  return out
}

async function loadExpenses() {
  loading.value = true
  try {
    expenses.value = await api<ExpenseRow[]>('/api/v1/finance/expenses')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function loadInvoices() { invoices.value = await api<InvoiceRow[]>('/api/v1/finance/invoices').catch(() => []) }
async function loadBudgets() { budgets.value = await api<BudgetRow[]>('/api/v1/finance/budgets').catch(() => []) }
async function loadPayments() { payments.value = await api<PaymentRow[]>('/api/v1/finance/payments').catch(() => []) }

async function save() {
  const items = form.items
    .filter((it) => it.name && it.yuan > 0)
    .map((it) => ({ name: it.name, amount: Math.round(it.yuan * 100) }))
  if (!form.reason || !items.length) {
    ElMessage.warning('事由与明细必填')
    return
  }
  try {
    await api('/api/v1/finance/expenses', {
      method: 'POST',
      body: { dept_id: form.dept_id, reason: form.reason, items },
    })
    ElMessage.success('草稿已保存')
    dlg.value = false
    Object.assign(form, { dept_id: '', reason: '', items: [{ name: '', yuan: 10 }] })
    await loadExpenses()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function submit(row: ExpenseRow) {
  try {
    await api(`/api/v1/finance/expenses/${row.id}/submit`, { method: 'POST' })
    ElMessage.success('已提交审批')
    await loadExpenses()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function pay(row: ExpenseRow) {
  try {
    await api('/api/v1/finance/payments', {
      method: 'POST',
      body: { expense_id: row.id, payee: '报销人' },
    })
    ElMessage.success('已付款')
    await Promise.all([loadExpenses(), loadPayments()])
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function saveInvoice() {
  if (!invForm.code || invForm.amount <= 0) return
  try {
    await api('/api/v1/finance/invoices', { method: 'POST', body: { ...invForm } })
    ElMessage.success('已登记')
    invDlg.value = false
    Object.assign(invForm, { code: '', title: '', tax_no: '', amount: 1000, date: '' })
    await loadInvoices()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function saveBudget() {
  if (!budgetDept.value) {
    ElMessage.warning('选择部门')
    return
  }
  try {
    await api(`/api/v1/finance/budgets/${budgetDept.value}`, {
      method: 'PUT',
      body: { limit: budgetLimit.value },
    })
    ElMessage.success('已设置')
    await loadBudgets()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

onMounted(async () => {
  await loadExpenses()
  const tree = await api<DeptRow[]>('/api/v1/org/depts/tree').catch(() => [])
  deptFlat.value = flatten(tree, [])
})
</script>

<template>
  <div class="designer">
    <!-- 工具栏 -->
    <div class="toolbar">
      <div class="toolbar-left">
        <el-button size="small" @click="$router.back()">← 返回</el-button>
        <el-divider direction="vertical" />
        <span class="def-name">{{ form.name || '新建流程' }}</span>
        <el-tag size="small" :type="form.status === 'active' ? 'success' : 'info'">{{ form.status }}</el-tag>
      </div>
      <div class="toolbar-right">
        <el-button size="small" @click="addStep('approval')">+ 审批步骤</el-button>
        <el-button size="small" @click="addStep('condition')">+ 条件分支</el-button>
        <el-button size="small" @click="addStep('parallel')">+ 并行网关</el-button>
        <el-button size="small" type="primary" @click="save">保存</el-button>
      </div>
    </div>

    <div class="main">
      <!-- 左侧：基本信息 -->
      <div class="panel-left">
        <el-form label-position="top" size="small">
          <el-form-item label="流程 Key">
            <el-input v-model="form.key" :disabled="!!defId" placeholder="如 expense" />
          </el-form-item>
          <el-form-item label="流程名称">
            <el-input v-model="form.name" placeholder="如 报销审批" />
          </el-form-item>
          <el-form-item label="分类">
            <el-input v-model="form.category" placeholder="如 finance" />
          </el-form-item>
          <el-form-item label="状态">
            <el-select v-model="form.status">
              <el-option label="启用" value="active" />
              <el-option label="停用" value="disabled" />
            </el-select>
          </el-form-item>
          <el-divider content-position="left">通知配置</el-divider>
          <el-form-item>
            <el-checkbox v-model="form.notify.step_enter">待办通知</el-checkbox>
            <el-checkbox v-model="form.notify.approved">通过通知</el-checkbox>
            <el-checkbox v-model="form.notify.rejected">驳回通知</el-checkbox>
            <el-checkbox v-model="form.notify.canceled">撤回通知</el-checkbox>
            <el-checkbox v-model="form.notify.cc">抄送通知</el-checkbox>
          </el-form-item>
        </el-form>
      </div>

      <!-- 中间：LogicFlow 画布 -->
      <div class="canvas-wrap">
        <div ref="canvasRef" class="canvas" />
      </div>

      <!-- 右侧：配置面板 -->
      <div class="panel-right">
        <div class="panel-title">{{ panelTitle }}</div>

        <div v-if="!sel" class="hint">点击画布节点进行配置</div>

        <!-- 条件网关配置 -->
        <template v-else-if="isGateway && topStep?.type === 'condition'">
          <el-form label-position="top" size="small">
            <el-form-item label="网关名称">
              <el-input v-model="topStep.name" @input="renderSteps" />
            </el-form-item>
          </el-form>
          <div class="rule" v-for="(r, ri) in topStep.rules ?? []" :key="ri">
            <div class="rule-head">
              <span>规则 {{ ri + 1 }}</span>
              <el-button text type="danger" size="small" @click="removeRule(ri)">删除</el-button>
            </div>
            <el-select v-model="r.to" size="small" style="width: 100%; margin-bottom: 6px"
              @change="renderSteps">
              <el-option v-for="(s, j) in steps" :key="j" :value="j" :disabled="j <= sel.step"
                :label="`${j + 1}. ${s.name || '未命名'}`" />
              <el-option label="结束流程" :value="-1" />
            </el-select>
            <div v-if="!r.cond.length" class="rule-default">默认分支（以上条件均不满足时）</div>
            <div class="cond-row" v-for="(c, ci) in r.cond" :key="ci">
              <el-input v-model="c.field" placeholder="变量" size="small" style="width: 74px" />
              <el-select v-model="c.op" size="small" style="width: 76px">
                <el-option v-for="o in ops" :key="o.value" :label="o.label" :value="o.value" />
              </el-select>
              <el-input v-model="c.value" placeholder="值" size="small" style="flex: 1" />
              <el-button text type="danger" size="small" @click="r.cond.splice(ci, 1)">×</el-button>
            </div>
            <el-button size="small" text @click="r.cond.push({ field: 'amount', op: 'gt', value: '' })">
              + 条件（AND）
            </el-button>
          </div>
          <el-button size="small" @click="addRule">+ 添加规则</el-button>
          <div class="tip">规则按顺序求值，首条命中生效；必须保留一条默认分支。</div>
        </template>

        <!-- 并行网关配置 -->
        <template v-else-if="isGateway && topStep?.type === 'parallel'">
          <el-form label-position="top" size="small">
            <el-form-item label="网关名称">
              <el-input v-model="topStep.name" @input="renderSteps" />
            </el-form-item>
          </el-form>
          <div class="branch" v-for="(br, bi) in topStep.branches ?? []" :key="bi">
            <div class="rule-head">
              <span>分支 {{ bi + 1 }}</span>
              <el-button text type="danger" size="small" :disabled="(topStep.branches?.length || 0) <= 1"
                @click="removeBranch(bi)">删除</el-button>
            </div>
            <div class="branch-step" v-for="(sub, si) in br" :key="si">
              <el-button text size="small" @click="selectBranchStep(bi, si)">
                {{ si + 1 }}. {{ sub.name || '未命名' }} ›
              </el-button>
              <el-button text type="danger" size="small" :disabled="br.length <= 1"
                @click="removeBranchStep(bi, si)">×</el-button>
            </div>
            <el-button size="small" text @click="addBranchStep(bi)">+ 分支步骤</el-button>
          </div>
          <el-button size="small" @click="addBranch">+ 添加分支</el-button>
          <div class="tip">各分支并行推进，全部分支完成后继续后续步骤；分支内不支持嵌套网关。</div>
        </template>

        <!-- 审批步骤配置 -->
        <template v-else-if="selectedStep">
          <div v-if="isSubStep" class="crumb">
            <el-button text size="small" @click="backToParallel">← 返回并行网关</el-button>
          </div>
          <el-form label-position="top" size="small">
            <el-form-item v-if="!isSubStep" label="步骤类型">
              <el-select v-model="stepType">
                <el-option label="审批步骤" value="approval" />
                <el-option label="条件分支" value="condition" />
                <el-option label="并行网关" value="parallel" />
              </el-select>
            </el-form-item>
            <el-form-item label="步骤名称">
              <el-input v-model="selectedStep.name" @input="renderSteps" />
            </el-form-item>
            <el-form-item label="审批模式">
              <el-select v-model="selectedStep.mode">
                <el-option label="会签（全部同意）" value="countersign" />
                <el-option label="或签（一人同意）" value="or-sign" />
                <el-option label="依次（按序逐人）" value="sequential" />
              </el-select>
            </el-form-item>
            <el-form-item label="审批人表达式">
              <el-select v-model="exprScheme" @change="onSchemeChange" style="margin-bottom: 8px">
                <el-option label="指定用户" value="user" />
                <el-option label="角色下全部用户" value="role" />
                <el-option label="部门负责人" value="dept-head" />
                <el-option label="上级部门负责人" value="dept-head-level" />
                <el-option label="部门内某角色" value="role-in-dept" />
                <el-option label="直接输入用户ID" value="bare" />
              </el-select>
              <el-select v-if="exprScheme === 'user' || exprScheme === 'bare'" v-model="exprArg"
                multiple filterable placeholder="选择用户" style="width: 100%">
                <el-option v-for="u in users" :key="u.id" :value="u.id" :label="u.username" />
              </el-select>
              <el-input v-if="exprScheme === 'role'" v-model="exprArg" placeholder="角色 code，如 finance" />
              <el-input-number v-if="exprScheme === 'dept-head-level'" v-model="exprLevel" :min="1" :max="10" />
              <el-input v-if="exprScheme === 'role-in-dept'" v-model="exprArg" placeholder="角色 code，如 manager" />
              <div class="expr-preview">表达式：<code>{{ computedExpr }}</code></div>
            </el-form-item>
            <el-divider />
            <el-button type="danger" size="small" @click="removeCurrent">
              {{ isSubStep ? '删除此分支步骤' : '删除此步骤' }}
            </el-button>
          </el-form>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onBeforeUnmount, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import LogicFlow from '@logicflow/core'
import '@logicflow/core/dist/index.css'
import { ElMessage } from '../element'
import { api, errMsg } from '../api'

// ---------- 类型 ----------
type StepType = 'approval' | 'condition' | 'parallel'
type StepMode = 'countersign' | 'or-sign' | 'sequential'
interface Cond { field: string; op: string; value: string }
interface Rule { to: number; cond: Cond[] }
interface WfStep {
  name: string
  approvers: string[]
  approver_expr: string
  mode: StepMode
  type?: StepType
  rules?: Rule[]
  branches?: WfStep[][]
}
interface NotifySpec {
  step_enter: boolean; approved: boolean; rejected: boolean; canceled: boolean; cc: boolean
}
interface UserRow { id: string; username: string }
interface Sel { step: number; branch?: number; sub?: number }

const ops = [
  { value: 'eq', label: '等于' }, { value: 'ne', label: '不等于' },
  { value: 'gt', label: '大于' }, { value: 'ge', label: '≥' },
  { value: 'lt', label: '小于' }, { value: 'le', label: '≤' },
  { value: 'in', label: '属于' }, { value: 'contains', label: '包含' },
]

// ---------- 路由 ----------
const route = useRoute()
const router = useRouter()
const defId = route.params.id as string | undefined

// ---------- 状态 ----------
const users = ref<UserRow[]>([])
const form = reactive({
  key: '', name: '', category: '', status: 'active',
  notify: { step_enter: true, approved: true, rejected: true, canceled: false, cc: true } as NotifySpec,
})

const steps = ref<WfStep[]>([])
const sel = ref<Sel | null>(null)

const topStep = computed(() => (sel.value ? steps.value[sel.value.step] : null))
const isSubStep = computed(() => sel.value?.branch !== undefined)
const isGateway = computed(() => {
  const t = topStep.value?.type
  return !!t && t !== 'approval' && !isSubStep.value
})
const selectedStep = computed(() => {
  const s = sel.value
  if (!s) return null
  if (s.branch === undefined) return steps.value[s.step] || null
  return steps.value[s.step]?.branches?.[s.branch]?.[s.sub!] || null
})
const panelTitle = computed(() => {
  if (!sel.value) return '配置'
  if (isSubStep.value) return '分支步骤配置'
  return topStep.value?.type === 'condition' ? '条件分支配置'
    : topStep.value?.type === 'parallel' ? '并行网关配置' : '步骤配置'
})

const stepType = computed<StepType>({
  get: () => topStep.value?.type || 'approval',
  set: (v: StepType) => {
    if (!topStep.value) return
    topStep.value.type = v
    onTypeChange()
  },
})

// ---------- 表达式 ----------
const exprScheme = ref('user')
const exprArg = ref<any>([])
const exprLevel = ref(1)
const computedExpr = computed(() => {
  if (!selectedStep.value) return ''
  switch (exprScheme.value) {
    case 'user': return (exprArg.value as string[]).map(id => `user:${id}`).join(',')
    case 'role': return `role:${exprArg.value}`
    case 'dept-head': return 'dept-head'
    case 'dept-head-level': return `dept-head:${exprLevel.value}`
    case 'role-in-dept': return `role-in-dept:${exprArg.value}`
    case 'bare': return (exprArg.value as string[]).join(',')
    default: return ''
  }
})

function applyCurrentExpr() {
  const st = selectedStep.value
  if (!st || (st.type && st.type !== 'approval')) return
  st.approver_expr = computedExpr.value
  if (exprScheme.value === 'bare') {
    st.approvers = [...exprArg.value]
    st.approver_expr = ''
  }
}

function onSchemeChange() {
  exprArg.value = exprScheme.value === 'user' || exprScheme.value === 'bare' ? [] : ''
}

function syncExprFromStep(st: WfStep) {
  const expr = st.approver_expr || ''
  if (!expr) {
    if (st.approvers?.length) {
      exprScheme.value = 'bare'
      exprArg.value = [...st.approvers]
    } else {
      exprScheme.value = 'user'
      exprArg.value = []
    }
    return
  }
  const first = expr.split(',')[0].trim()
  if (first.startsWith('user:')) {
    exprScheme.value = 'user'
    exprArg.value = expr.split(',').map(s => s.trim().replace('user:', ''))
  } else if (first.startsWith('role-in-dept:')) {
    exprScheme.value = 'role-in-dept'
    exprArg.value = first.replace('role-in-dept:', '')
  } else if (first.startsWith('role:')) {
    exprScheme.value = 'role'
    exprArg.value = first.replace('role:', '')
  } else if (first.startsWith('dept-head:')) {
    exprScheme.value = 'dept-head-level'
    exprLevel.value = parseInt(first.replace('dept-head:', '')) || 1
  } else if (first === 'dept-head') {
    exprScheme.value = 'dept-head'
  } else {
    exprScheme.value = 'bare'
    exprArg.value = expr.split(',').map(s => s.trim())
  }
}

// ---------- LogicFlow ----------
const canvasRef = ref<HTMLDivElement>()
let lf: LogicFlow | null = null

function initCanvas() {
  if (!canvasRef.value) return
  const dark = document.documentElement.classList.contains('dark')
  lf = new LogicFlow({
    container: canvasRef.value,
    grid: { visible: true, type: 'mesh', size: 20 },
    // 画布底色跟随当前主题，避免深色模式下面板与画布割裂
    background: { background: dark ? '#222839' : '#fafbfc' },
    edgeType: 'polyline',
    keyboard: { enabled: false },
    isSilentMode: false,
  })

  lf.on('node:click', ({ data }: any) => {
    const p = data.properties || {}
    if (p.kind === 'branch') {
      selectStep({ step: p.step, branch: p.branch, sub: p.sub })
    } else if (p.kind === 'approval' || p.kind === 'condition' || p.kind === 'parallel') {
      selectStep({ step: p.step })
    }
  })

  lf.on('blank:click', () => {
    applyCurrentExpr()
    sel.value = null
  })
}

function selectStep(next: Sel) {
  applyCurrentExpr()
  sel.value = next
  const st = selectedStep.value
  if (st) syncExprFromStep(st)
}

const seqStyle = { stroke: '#999', strokeWidth: 2 }
const gwStyle = { stroke: '#fa8c16', strokeWidth: 2 }

function ruleText(r: Rule) {
  if (!r.cond?.length) return '默认'
  return r.cond.map(c => `${c.field}${c.op}${c.value}`).join(' 且 ')
}

// 把 steps 渲染为节点图：审批步骤直连，条件网关按规则连线，
// 并行网关展开分支子链并在末端绘制汇聚节点。
function renderSteps() {
  if (!lf) return
  const nodes: any[] = []
  const edges: any[] = []
  const cx = 380
  const gapY = 110
  const branchDx = 220
  let cursorY = 60

  const nodeId = (i: number) => {
    const t = steps.value[i]?.type || 'approval'
    return t === 'condition' ? `gw-cond-${i}` : t === 'parallel' ? `gw-par-${i}` : `step-${i}`
  }

  nodes.push({
    id: 'start', type: 'circle', x: cx, y: cursorY, text: '发起',
    style: { fill: '#52c41a', stroke: '#52c41a', strokeWidth: 2, r: 28 },
  })
  cursorY += gapY

  const exits: Record<number, string[]> = {}


  steps.value.forEach((st, i) => {
    const t = st.type || 'approval'
    if (t === 'condition') {
      nodes.push({
        id: nodeId(i), type: 'diamond', x: cx, y: cursorY,
        text: `${i + 1}. ${st.name || '条件'}`,
        properties: { kind: 'condition', step: i },
        style: { fill: '#fff7e6', stroke: '#fa8c16', strokeWidth: 2, rx: 76, ry: 44 },
      })
      exits[i] = [] // 出口由规则边表达
      cursorY += gapY
    } else if (t === 'parallel') {
      const branches = st.branches || []
      nodes.push({
        id: nodeId(i), type: 'rect', x: cx, y: cursorY,
        text: `${i + 1}. ${st.name || '并行'}（${branches.length} 分支）`,
        properties: { kind: 'parallel', step: i },
        style: { fill: '#f9f0ff', stroke: '#722ed1', strokeWidth: 2, width: 210, height: 56, radius: 8 },
      })
      const maxLen = Math.max(1, ...branches.map(b => b.length))
      const lastIds: string[] = []
      branches.forEach((br, bi) => {
        const bx = cx + (bi - (branches.length - 1) / 2) * branchDx
        br.forEach((sub, si) => {
          const id = `step-${i}-b${bi}-s${si}`
          nodes.push({
            id, type: 'rect', x: bx, y: cursorY + gapY * (si + 1),
            text: sub.name || '未命名',
            properties: { kind: 'branch', step: i, branch: bi, sub: si },
            style: { fill: '#e6f4ff', stroke: '#1677ff', strokeWidth: 2, width: 170, height: 52, radius: 8 },
          })
          edges.push({
            id: `e-${id}`, type: 'polyline',
            sourceNodeId: si === 0 ? nodeId(i) : `step-${i}-b${bi}-s${si - 1}`,
            targetNodeId: id, style: seqStyle,
          })
        })
        if (br.length) lastIds.push(`step-${i}-b${bi}-s${br.length - 1}`)
      })
      const joinId = `join-${i}`
      const joinY = cursorY + gapY * (maxLen + 1)
      nodes.push({
        id: joinId, type: 'circle', x: cx, y: joinY, text: '汇聚',
        style: { fill: '#f0f0f0', stroke: '#722ed1', strokeWidth: 2, r: 26 },
      })
      lastIds.forEach(lid => edges.push({
        id: `e-${lid}-join`, type: 'polyline',
        sourceNodeId: lid, targetNodeId: joinId, style: seqStyle,
      }))
      exits[i] = [joinId]
      cursorY = joinY + gapY
    } else {
      nodes.push({
        id: nodeId(i), type: 'rect', x: cx, y: cursorY,
        text: `${i + 1}. ${st.name || '未命名'}`,
        properties: { kind: 'approval', step: i },
        style: { fill: '#e6f4ff', stroke: '#1677ff', strokeWidth: 2, width: 200, height: 56, radius: 8 },
      })
      exits[i] = [nodeId(i)]
      cursorY += gapY
    }
  })

  nodes.push({
    id: 'end', type: 'circle', x: cx, y: cursorY, text: '结束',
    style: { fill: '#ff4d4f', stroke: '#ff4d4f', strokeWidth: 2, r: 26 },
  })

  // 顺序边：上一步出口 → 当前步入口
  let prevExits = ['start']
  steps.value.forEach((st, i) => {
    const entry = nodeId(i)
    prevExits.forEach(pid => edges.push({
      id: `seq-${pid}-${entry}`, type: 'polyline',
      sourceNodeId: pid, targetNodeId: entry, style: seqStyle,
    }))
    // 条件网关的出口是规则边
    if (st.type === 'condition') {
      ;(st.rules || []).forEach((r, ri) => {
        const target = r.to < 0 || r.to >= steps.value.length ? 'end' : nodeId(r.to)
        if (target === entry) return
        edges.push({
          id: `cond-${i}-${ri}`, type: 'polyline',
          sourceNodeId: entry, targetNodeId: target,
          text: ruleText(r),
          style: gwStyle,
          textStyle: { fill: '#fa8c16', fontSize: 11 },
        })
      })
      prevExits = []
    } else {
      prevExits = exits[i] || []
    }
  })
  // 末端接入结束节点（条件网关已由规则边连接）
  prevExits.forEach(pid => edges.push({
    id: `seq-${pid}-end`, type: 'polyline',
    sourceNodeId: pid, targetNodeId: 'end', style: seqStyle,
  }))

  lf.render({ nodes, edges })
  nextTick(() => { lf?.fitView(100) })
}

// ---------- 步骤操作 ----------
function addStep(type: StepType) {
  const next: WfStep = { name: '', approvers: [], approver_expr: '', mode: 'countersign', type }
  if (type === 'condition') {
    next.name = '条件分支'
    next.rules = [
      { to: -1, cond: [{ field: 'amount', op: 'gt', value: '5000' }] },
      { to: -1, cond: [] },
    ]
  } else if (type === 'parallel') {
    next.name = '并行网关'
    next.branches = [[{ name: '分支步骤', approvers: [], approver_expr: '', mode: 'countersign' }]]
  } else {
    next.name = `步骤${steps.value.length + 1}`
    next.approver_expr = ''
  }
  steps.value.push(next)
  const idx = steps.value.length - 1
  if (type === 'parallel') {
    selectStep({ step: idx, branch: 0, sub: 0 })
  } else {
    selectStep({ step: idx })
  }
  renderSteps()
}

function removeCurrent() {
  const s = sel.value
  if (!s) return
  if (s.branch !== undefined) {
    removeBranchStep(s.branch, s.sub!)
    selectStep({ step: s.step })
    return
  }
  steps.value.splice(s.step, 1)
  sel.value = null
  renderSteps()
}

function addRule() {
  if (!topStep.value) return
  const next = Math.min(sel.value!.step + 1, steps.value.length - 1)
  topStep.value.rules = topStep.value.rules || []
  topStep.value.rules.push({ to: next >= 0 ? next : -1, cond: [{ field: 'amount', op: 'gt', value: '' }] })
}

function removeRule(ri: number) {
  topStep.value?.rules?.splice(ri, 1)
}

function addBranch() {
  if (!topStep.value) return
  topStep.value.branches = topStep.value.branches || []
  topStep.value.branches.push([{ name: '分支步骤', approvers: [], approver_expr: '', mode: 'countersign' }])
  renderSteps()
}

function removeBranch(bi: number) {
  const bs = topStep.value?.branches
  if (!bs || bs.length <= 1) return
  bs.splice(bi, 1)
  if (sel.value?.branch !== undefined) {
    if (sel.value.branch === bi) sel.value = { step: sel.value.step }
    else if (sel.value.branch > bi) sel.value.branch--
  }
  renderSteps()
}

function addBranchStep(bi: number) {
  const br = topStep.value?.branches?.[bi]
  if (!br) return
  br.push({ name: `步骤${br.length + 1}`, approvers: [], approver_expr: '', mode: 'countersign' })
  renderSteps()
}

function removeBranchStep(bi: number, si: number) {
  const br = topStep.value?.branches?.[bi]
  if (!br || br.length <= 1) return
  br.splice(si, 1)
  if (sel.value?.branch === bi && sel.value.sub !== undefined) {
    if (sel.value.sub >= br.length) sel.value.sub = br.length - 1
  }
  renderSteps()
}

function selectBranchStep(bi: number, si: number) {
  if (!sel.value) return
  selectStep({ step: sel.value.step, branch: bi, sub: si })
}

function backToParallel() {
  if (!sel.value) return
  selectStep({ step: sel.value.step })
}

function onTypeChange() {
  const st = topStep.value
  if (!st) return
  if (st.type === 'condition') {
    const next = sel.value!.step + 1 < steps.value.length ? sel.value!.step + 1 : -1
    st.rules = st.rules?.length ? st.rules : [{ to: next, cond: [] }]
    st.branches = undefined
    st.approvers = []
    st.approver_expr = ''
    st.mode = 'countersign'
  } else if (st.type === 'parallel') {
    st.branches = st.branches?.length
      ? st.branches
      : [[{ name: '分支步骤', approvers: [], approver_expr: '', mode: 'countersign' }]]
    st.rules = undefined
    st.approvers = []
    st.approver_expr = ''
    st.mode = 'countersign'
  } else {
    st.rules = undefined
    st.branches = undefined
    st.type = 'approval'
  }
  sel.value = { step: sel.value!.step }
  renderSteps()
}

// ---------- 校验 ----------
function validateStep(st: WfStep, label: string): string | null {
  const t = st.type || 'approval'
  if (t === 'approval') {
    if (!st.approver_expr && !st.approvers?.length) return `${label}需要配置审批人`
    return null
  }
  if (t === 'condition') {
    const rules = st.rules || []
    if (!rules.length) return `${label}至少需要一条规则`
    if (!rules.some(r => !r.cond.length)) return `${label}必须保留一条默认分支`
    for (const r of rules) {
      if (r.to !== -1 && (r.to <= steps.value.indexOf(st) || r.to >= steps.value.length)) {
        return `${label}的跳转目标必须是后续步骤`
      }
      for (const c of r.cond) {
        if (!c.field) return `${label}的条件变量不能为空`
      }
    }
    return null
  }
  if (t === 'parallel') {
    const bs = st.branches || []
    if (!bs.length) return `${label}至少需要一条分支`
    for (const br of bs) {
      if (!br.length) return `${label}存在空分支`
      for (const sub of br) {
        if (!sub.approver_expr && !sub.approvers?.length) return `${label}内「${sub.name || '?'}」需要配置审批人`
      }
    }
    return null
  }
  return null
}

// ---------- 加载 / 保存 ----------
const normalize = (s: any): WfStep => ({
  name: s.name || '',
  approvers: s.approvers || [],
  approver_expr: s.approver_expr || '',
  mode: s.mode || 'countersign',
  type: s.type === 'condition' ? 'condition' : s.type === 'parallel' ? 'parallel' : undefined,
  rules: s.rules?.map((r: any) => ({
    to: r.to,
    cond: (r.cond || []).map((c: any) => ({ field: c.field, op: c.op, value: c.value })),
  })),
  branches: s.branches?.map((br: any[]) => br.map(normalize)),
})

async function load() {
  try {
    users.value = await api<UserRow[]>('/api/v1/identity/users').catch(() => [])
    if (defId) {
      const defs = await api<any[]>('/api/v1/wf/definitions')
      const def = defs.find(d => d.id === defId)
      if (def) {
        form.key = def.key
        form.name = def.name
        form.category = def.category
        form.status = def.status
        if (def.notify) form.notify = def.notify
        steps.value = (def.steps || []).map(normalize)
      }
    } else {
      steps.value = [{ name: '部门主管审批', approvers: [], approver_expr: 'dept-head', mode: 'countersign' }]
    }
    renderSteps()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function save() {
  if (!form.key || !form.name) {
    ElMessage.warning('Key 和名称必填')
    return
  }
  if (!steps.value.length) {
    ElMessage.warning('至少需要一个步骤')
    return
  }
  applyCurrentExpr()
  for (let i = 0; i < steps.value.length; i++) {
    const err = validateStep(steps.value[i], `第 ${i + 1} 步「${steps.value[i].name || '未命名'}」`)
    if (err) {
      ElMessage.warning(err)
      return
    }
  }
  const payload = steps.value.map(st => {
    const out: any = { name: st.name, mode: st.mode || 'countersign' }
    if (st.type === 'condition') {
      out.type = 'condition'
      out.rules = st.rules
    } else if (st.type === 'parallel') {
      out.type = 'parallel'
      out.branches = st.branches?.map(br => br.map(s => ({
        name: s.name, mode: s.mode || 'countersign',
        approvers: s.approvers || [], approver_expr: s.approver_expr || '',
      })))
    } else {
      out.approvers = st.approvers || []
      out.approver_expr = st.approver_expr || ''
    }
    return out
  })
  try {
    if (defId) {
      await api(`/api/v1/wf/definitions/${defId}`, {
        method: 'PATCH',
        body: { name: form.name, steps: payload, notify: form.notify, status: form.status },
      })
    } else {
      await api('/api/v1/wf/definitions', {
        method: 'POST',
        body: { key: form.key, name: form.name, category: form.category, steps: payload, notify: form.notify },
      })
    }
    ElMessage.success('已保存')
    router.push('/wfdefs')
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

onMounted(() => {
  initCanvas()
  load()
})

onBeforeUnmount(() => {
  lf?.destroy()
})
</script>

<style scoped>
.designer { display: flex; flex-direction: column; height: 100vh; background: var(--ape-body-bg); }
.toolbar {
  display: flex; justify-content: space-between; align-items: center;
  padding: 10px 16px; background: var(--ape-card-bg);
  border-bottom: 1px solid var(--ape-border);
}
.toolbar-left, .toolbar-right { display: flex; align-items: center; gap: 8px; }
.def-name { font-weight: 600; font-size: 15px; color: var(--ape-text); }
.main { display: flex; flex: 1; overflow: hidden; }
.panel-left {
  width: 250px; padding: 16px; background: var(--ape-card-bg);
  border-right: 1px solid var(--ape-border); overflow-y: auto;
}
.canvas-wrap { flex: 1; position: relative; background: var(--ape-card-bg); }
.canvas { width: 100%; height: 100%; }
.panel-right {
  width: 290px; padding: 16px; background: var(--ape-card-bg);
  border-left: 1px solid var(--ape-border); overflow-y: auto;
}
.panel-title { font-weight: 600; margin-bottom: 12px; font-size: 14px; color: var(--ape-text); }
.hint { font-size: 12px; color: var(--ape-text-secondary); }
.crumb { margin-bottom: 8px; }
.tip { margin-top: 10px; font-size: 12px; color: var(--ape-text-secondary); line-height: 1.6; }
.expr-preview {
  margin-top: 8px; padding: 6px 8px; background: var(--ape-body-bg);
  border-radius: 4px; font-size: 12px; color: var(--ape-text-regular);
}
.expr-preview code { color: var(--ape-primary); }
.rule, .branch {
  border: 1px solid var(--ape-border); border-radius: 8px; padding: 8px; margin-bottom: 10px;
}
.rule-head {
  display: flex; justify-content: space-between; align-items: center;
  font-size: 12px; font-weight: 600; color: var(--ape-text-regular); margin-bottom: 6px;
}
.rule-default { font-size: 12px; color: var(--ape-text-secondary); margin-bottom: 6px; }
.cond-row { display: flex; gap: 4px; align-items: center; margin-bottom: 4px; }
.branch-step { display: flex; justify-content: space-between; align-items: center; }
</style>

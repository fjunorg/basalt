<template>
  <div class="page">
    <div class="toolbar">
      <el-button size="small" @click="load">刷新</el-button>
      <span class="hint">90 秒滑动窗口实时快照，30s 自动刷新；QPS 趋势为本页采样</span>
    </div>

    <el-row :gutter="16">
      <el-col :span="6"><el-card><template #header>QPS</template>
        <div class="metric">{{ m?.window.qps?.toFixed(2) ?? '-' }}</div>
        <div class="hint">窗口 {{ m?.window.requests ?? '-' }} 次请求 / {{ m?.window.window_seconds ?? '-' }}s</div>
      </el-card></el-col>
      <el-col :span="6"><el-card><template #header>错误率（5xx+429）</template>
        <div class="metric" :style="errorRate > 0.05 ? 'color: var(--semi-color-danger)' : ''">{{ m ? (m.window.error_rate * 100).toFixed(2) + '%' : '-' }}</div>
        <el-progress :percentage="Math.min(100, (m?.window.error_rate ?? 0) * 100)" :show-text="false"
          :status="(m?.window.error_rate ?? 0) > 0.05 ? 'exception' : undefined" style="margin-top: 6px" />
      </el-card></el-col>
      <el-col :span="6"><el-card><template #header>延迟分位</template>
        <div class="hint" style="font-size: 13px">P50 {{ m?.window.p50_ms?.toFixed(1) ?? '-' }}ms</div>
        <div style="font-size: 16px; font-weight: 600">P95 {{ m?.window.p95_ms?.toFixed(1) ?? '-' }}ms</div>
        <div class="hint" style="font-size: 13px">P99 {{ m?.window.p99_ms?.toFixed(1) ?? '-' }}ms</div>
      </el-card></el-col>
      <el-col :span="6"><el-card><template #header>QPS 趋势（本页采样）</template>
        <svg viewBox="0 0 200 60" preserveAspectRatio="none" style="width: 100%; height: 60px">
          <polyline :points="trendPoints" fill="none" stroke="var(--semi-color-primary)" stroke-width="2" />
          <line x1="0" y1="59" x2="200" y2="59" stroke="var(--semi-color-border)" stroke-width="1" />
        </svg>
        <div class="hint">{{ trend.length }} 个采样点</div>
      </el-card></el-col>
    </el-row>

    <el-row :gutter="16" style="margin-top: 16px">
      <el-col :span="12"><el-card><template #header>进程指标</template>
        <el-descriptions :column="2" size="small" border>
          <el-descriptions-item label="Goroutines">{{ m?.process.goroutines ?? '-' }}</el-descriptions-item>
          <el-descriptions-item label="Go 版本">{{ m?.process.go_version ?? '-' }}</el-descriptions-item>
          <el-descriptions-item label="堆内存">{{ m?.process.heap_alloc_mb?.toFixed(1) ?? '-' }} MB</el-descriptions-item>
          <el-descriptions-item label="系统内存">{{ m?.process.sys_mb?.toFixed(1) ?? '-' }} MB</el-descriptions-item>
          <el-descriptions-item label="GC 次数">{{ m?.process.num_gc ?? '-' }}</el-descriptions-item>
        </el-descriptions>
      </el-card></el-col>
      <el-col :span="12"><el-card><template #header>采集器状态</template>
        <el-descriptions :column="1" size="small" border>
          <el-descriptions-item label="采样模式">{{ collectorMode }}</el-descriptions-item>
          <el-descriptions-item label="缓冲中">{{ m?.collector.buffered ?? '-' }}</el-descriptions-item>
          <el-descriptions-item label="丢弃">
            <el-tag size="small" :type="(m?.collector.dropped ?? 0) > 0 ? 'danger' : 'success'">{{ m?.collector.dropped ?? 0 }}</el-tag>
            <span class="hint" style="margin-left: 6px">dropped &gt; 0 表示采集背压丢弃（通道满）</span>
          </el-descriptions-item>
        </el-descriptions>
      </el-card></el-col>
    </el-row>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, type OpMetrics } from '../api'

// 契约 oplog-api-contract.md §2：GET /oplog/metrics 实时快照
const m = ref<OpMetrics | null>(null)
const trend = ref<number[]>([])
let timer: ReturnType<typeof setInterval> | null = null

const errorRate = computed(() => m.value?.window.error_rate ?? 0)
const collectorMode = computed(() => {
  const v = { all: '全量采集', write: '仅变更操作', off: '关闭' }[m.value?.collector.mode || ''] || m.value?.collector.mode || '-'
  return v
})
// 本页采样折线（200x60 viewBox 归一化；窗口 40 点滚动）
const trendPoints = computed(() => {
  if (!trend.value.length) return ''
  const max = Math.max(...trend.value, 0.01)
  const n = Math.min(trend.value.length, 40)
  const pts = trend.value.slice(-n)
  return pts.map((v, i) => {
    const x = (i / Math.max(n - 1, 1)) * 200
    const y = 55 - (v / max) * 50
    return `${x.toFixed(1)},${y.toFixed(1)}`
  }).join(' ')
})

async function load() {
  m.value = await api<OpMetrics>('/api/v1/oplog/metrics').catch(() => null)
  if (m.value) trend.value.push(m.value.window.qps)
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.metric { font-size: 28px; font-weight: 600; color: var(--semi-color-text-0); }
.hint { color: var(--semi-color-text-2); font-size: 12px; }
</style>

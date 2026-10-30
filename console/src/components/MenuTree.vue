<template>
  <!-- 递归渲染后端下发的菜单树（契约 menu-api-contract.md §1 MenuNode）：
       目录节点（children 非空）→ el-sub-menu；叶子 → el-menu-item（path 导航） -->
  <template v-for="node in nodes" :key="node.id">
    <el-sub-menu v-if="node.children?.length" :index="node.id">
      <template #title>
        <el-icon v-if="iconOf(node.icon)"><component :is="iconOf(node.icon)" /></el-icon>
        <span>{{ node.name }}</span>
      </template>
      <MenuTree :nodes="node.children" />
    </el-sub-menu>
    <el-menu-item v-else :index="node.path">
      <el-icon v-if="iconOf(node.icon)"><component :is="iconOf(node.icon)" /></el-icon>
      <template #title>{{ node.name }}</template>
    </el-menu-item>
  </template>
</template>

<script setup lang="ts">
// 图标名 → Element Plus 图标组件。契约 icon 字段为 EP 图标名；
// 为守住按需导入体积（icons 全量 namespace 会整包打入），此处仅
// 显式注册管理台常用图标白名单——未注册名不渲染图标（菜单仍可用）。
import {
  Odometer, Setting, Briefcase, Monitor, Menu, User, UserFilled, Avatar,
  Lock, Key, OfficeBuilding, Connection, Share, Collection, Files, Folder,
  Document, Tickets, Notebook, Bell, Message, ChatDotRound, Calendar,
  Wallet, Money, CreditCard, Coin, DataAnalysis, TrendCharts, PieChart,
  List, Grid, Operation, Tools, SetUp, Search, InfoFilled, WarningFilled,
  CircleCheck, Timer, AlarmClock, Link, Platform, Cpu,
} from '@element-plus/icons-vue'
import type { Component } from 'vue'
import type { MenuNode } from '../api'

defineProps<{ nodes: MenuNode[] }>()
defineOptions({ name: 'MenuTree' }) // 递归组件需具名自引用

const ICONS: Record<string, Component> = {
  Odometer, Setting, Briefcase, Monitor, Menu, User, UserFilled, Avatar,
  Lock, Key, OfficeBuilding, Connection, Share, Collection, Files, Folder,
  Document, Tickets, Notebook, Bell, Message, ChatDotRound, Calendar,
  Wallet, Money, CreditCard, Coin, DataAnalysis, TrendCharts, PieChart,
  List, Grid, Operation, Tools, SetUp, Search, InfoFilled, WarningFilled,
  CircleCheck, Timer, AlarmClock, Link, Platform, Cpu,
}

const iconOf = (name?: string): Component | null => (name ? ICONS[name] || null : null)
</script>

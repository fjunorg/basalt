<template>
  <div class="page" style="display: flex; justify-content: center; padding-top: 6vh">
    <el-result icon="warning" title="无权访问该页面" :sub-title="subTitle">
      <template #extra>
        <div v-if="needPerms.length" style="margin-bottom: 18px">
          <div style="color: var(--semi-color-text-2); font-size: 13px; margin-bottom: 6px">所需权限点（满足任一即可）：</div>
          <el-tag v-for="p in needPerms" :key="p" size="small" type="warning" style="margin: 2px">{{ p }}</el-tag>
        </div>
        <el-button type="primary" @click="$router.push('/overview')">返回概览</el-button>
      </template>
    </el-result>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()
const needPerms = computed(() => String(route.query.need || '').split(',').filter(Boolean))
const subTitle = computed(() => {
  const to = String(route.query.to || '')
  return to
    ? `您访问的页面（${to}）需要上述权限，当前账号未持有。如需开通请联系管理员分配对应角色。`
    : '当前账号未持有该页面所需权限。如需开通请联系管理员分配对应角色。'
})
</script>

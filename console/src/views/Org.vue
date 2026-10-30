<template>
  <div class="page">
    <el-tabs v-model="tab">
      <el-tab-pane label="部门管理" name="dept">
        <div class="toolbar">
          <el-button v-if="can('org:dept:manage')" type="primary" @click="openDept()">新建部门</el-button>
          <el-button @click="loadDepts">刷新</el-button>
        </div>
        <el-table :data="depts" border row-key="id" default-expand-all v-loading="loading" size="small">
          <el-table-column prop="name" label="部门名称" width="240" />
          <el-table-column prop="sort" label="排序" width="80" />
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag :type="row.status === 'active' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="id" label="ID" show-overflow-tooltip />
          <el-table-column label="操作" width="160" fixed="right">
            <template #default="{ row }">
              <el-button v-if="can('org:dept:manage')" size="small" @click="openDept(row)">编辑</el-button>
              <el-button v-if="can('org:dept:manage')" size="small" type="danger" @click="delDept(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <el-tab-pane label="岗位管理" name="post">
        <div class="toolbar">
          <el-button v-if="can('org:post:manage')" type="primary" @click="postDlg = true">新建岗位</el-button>
          <el-button @click="loadPosts">刷新</el-button>
        </div>
        <el-table :data="posts" border stripe v-loading="loading" size="small">
          <el-table-column prop="code" label="代码" width="140" />
          <el-table-column prop="name" label="名称" width="180" />
          <el-table-column prop="sort" label="排序" width="80" />
          <el-table-column prop="status" label="状态" width="90" />
          <el-table-column label="操作" width="100" fixed="right">
            <template #default="{ row }">
              <el-button v-if="can('org:post:manage')" size="small" type="danger" @click="delPost(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="deptDlg" :title="editing ? '编辑部门' : '新建部门'" width="420">
      <el-form label-width="80">
        <el-form-item label="上级部门">
          <el-tree-select v-model="deptForm.parent_id" :data="deptOptions" check-strictly clearable
            node-key="id" :props="{ label: 'name', children: 'children' }" placeholder="留空为根部门" style="width: 100%" />
        </el-form-item>
        <el-form-item label="名称"><el-input v-model="deptForm.name" /></el-form-item>
        <el-form-item label="排序"><el-input-number v-model="deptForm.sort" :min="0" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="deptDlg = false">取消</el-button>
        <el-button type="primary" @click="saveDept">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="postDlg" title="新建岗位" width="400">
      <el-form label-width="80">
        <el-form-item label="代码"><el-input v-model="postForm.code" placeholder="如 dev、hr" /></el-form-item>
        <el-form-item label="名称"><el-input v-model="postForm.name" /></el-form-item>
        <el-form-item label="排序"><el-input-number v-model="postForm.sort" :min="0" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="postDlg = false">取消</el-button>
        <el-button type="primary" @click="savePost">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../element'
import { api, perms, hasPerm, errMsg } from '../api'

interface DeptRow { id: string; parent_id: string; name: string; sort: number; status: string; children?: DeptRow[] }
interface PostRow { id: string; code: string; name: string; sort: number; status: string }

const can = (p: string) => hasPerm(perms.get(), p)

const tab = ref('dept')
const depts = ref<DeptRow[]>([])
const posts = ref<PostRow[]>([])
const loading = ref(false)
const deptDlg = ref(false)
const postDlg = ref(false)
const editing = ref<DeptRow | null>(null)
const deptForm = reactive({ parent_id: '', name: '', sort: 0 })
const postForm = reactive({ code: '', name: '', sort: 0 })

const deptOptions = computed(() => depts.value)

async function loadDepts() {
  loading.value = true
  try {
    depts.value = await api<DeptRow[]>('/api/v1/org/depts/tree')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function loadPosts() {
  loading.value = true
  try {
    posts.value = await api<PostRow[]>('/api/v1/org/posts')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function openDept(row?: DeptRow) {
  editing.value = row || null
  Object.assign(deptForm, row ? { parent_id: row.parent_id, name: row.name, sort: row.sort }
    : { parent_id: '', name: '', sort: 0 })
  deptDlg.value = true
}

async function saveDept() {
  if (!deptForm.name) return
  try {
    if (editing.value) {
      await api(`/api/v1/org/depts/${editing.value.id}`, { method: 'PATCH', body: { ...deptForm } })
    } else {
      await api('/api/v1/org/depts', { method: 'POST', body: { ...deptForm } })
    }
    ElMessage.success('已保存')
    deptDlg.value = false
    await loadDepts()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function delDept(row: DeptRow) {
  try {
    await ElMessageBox.confirm(`删除部门「${row.name}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/org/depts/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await loadDepts()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

async function savePost() {
  if (!postForm.code || !postForm.name) return
  try {
    await api('/api/v1/org/posts', { method: 'POST', body: { ...postForm } })
    ElMessage.success('已保存')
    postDlg.value = false
    Object.assign(postForm, { code: '', name: '', sort: 0 })
    await loadPosts()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function delPost(row: PostRow) {
  try {
    await ElMessageBox.confirm(`删除岗位「${row.name}」？`, '确认', { type: 'warning' })
    await api(`/api/v1/org/posts/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await loadPosts()
  } catch (e) {
    if (e !== 'cancel') ElMessage.error(errMsg(e))
  }
}

onMounted(() => { loadDepts(); loadPosts() })
</script>

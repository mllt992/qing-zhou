<template>
  <div>
    <div class="page-head"><div><h2 class="page-title">用户组</h2><p class="page-sub">购买权限、专属套餐与成员关系管理</p></div><div class="page-actions"><n-button type="primary" @click="openForm()">创建用户组</n-button></div></div>
    <p class="section-note" style="margin:0 0 14px;">
      用户组决定<b>谁能购买某个套餐</b>。把用户加进组，再到「套餐管理」里给套餐勾选可购买的用户组，该套餐就只对组内成员可见、可买。
      未绑定任何用户组的套餐对所有人开放。（与「节点管理」里的节点分组无关，那个决定的是买到套餐后能用哪些节点。）
    </p>
    <div class="resource-overview">
      <div class="resource-metric"><b>{{ groups.length }}</b><span>全部用户组</span></div>
      <div class="resource-metric"><b>{{ groups.reduce((sum, g) => sum + (g.members || 0), 0) }}</b><span>成员关系总数</span></div>
      <div class="resource-metric"><b>{{ packages.filter(p => p.user_group_ids?.length).length }}</b><span>专属套餐</span></div>
    </div>

    <n-spin :show="loading">
      <div v-if="groups.length" class="card-grid">
        <div v-for="g in groups" :key="g.id" class="list-card">
          <div class="lc-head">
            <span class="lc-title">{{ g.name }}</span>
            <n-tag size="tiny" :bordered="false" type="info">{{ g.members || 0 }} 人</n-tag>
          </div>
          <div v-if="g.description" class="lc-meta" style="color:var(--text-3);">{{ g.description }}</div>
          <div class="lc-meta">
            <span class="kv">
              专属套餐
              <b>{{ packageNames(g.id) || '—' }}</b>
            </span>
          </div>
          <div class="lc-foot" style="flex-wrap:wrap;">
            <n-button size="tiny" type="primary" secondary @click="openMembers(g)">管理成员</n-button>
            <n-button size="tiny" @click="openForm(g)">编辑</n-button>
            <span style="flex:1;" />
            <n-button size="tiny" type="error" quaternary @click="handleDelete(g)">删除</n-button>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="暂无用户组" style="padding:40px 0;" />
    </n-spin>

    <!-- 创建 / 编辑 -->
    <n-modal v-model:show="showForm" preset="card" :title="editing ? '编辑用户组' : '创建用户组'" style="max-width:440px;">
      <n-form label-placement="left" label-width="80">
        <n-form-item label="名称"><n-input v-model:value="form.name" placeholder="如：内测组 / 亲友组" /></n-form-item>
        <n-form-item label="描述"><n-input v-model:value="form.description" placeholder="备注用途（可留空）" /></n-form-item>
        <n-form-item label="排序"><n-input-number v-model:value="form.sort_order" style="width:100%;" /></n-form-item>
      </n-form>
      <n-button type="primary" block :loading="saving" @click="handleSave">保存</n-button>
    </n-modal>

    <!-- 成员 -->
    <n-modal v-model:show="showMembers" preset="card" :title="`「${membersGroup?.name}」的成员`" style="max-width:560px;">
      <p style="font-size:13px;color:var(--text-2);margin-bottom:12px;">
        勾选后即加入该组，取消勾选即移出。移出后该用户不能再购买/续费本组专属套餐，但<b>已购买的套餐继续有效到期</b>。
      </p>
      <n-select
        v-model:value="memberIDs"
        :options="userOptions"
        :loading="searching"
        multiple
        filterable
        clearable
        remote
        :clear-filter-after-select="false"
        placeholder="搜索用户名 / 邮箱后选择"
        :max-tag-count="6"
        @search="handleSearchUsers"
      />
      <div style="margin-top:6px;font-size:12px;color:var(--text-3);line-height:1.5;">
        默认只列出最近 200 位用户；更早的用户请输入用户名或邮箱搜索。
      </div>
      <n-button type="primary" block style="margin-top:16px;" :loading="saving" @click="handleSaveMembers">保存成员</n-button>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import {
  NSpin, NButton, NModal, NForm, NFormItem, NInput, NInputNumber,
  NSelect, NTag, NEmpty, useMessage, useDialog
} from 'naive-ui'
import { apiList, apiPost, apiPut, apiDelete } from '@/api'

const message = useMessage()
const dialog = useDialog()

const groups = ref<any[]>([])
const packages = ref<any[]>([])
const loading = ref(false)
const saving = ref(false)

const showForm = ref(false)
const editing = ref<any>(null)
const form = reactive({ name: '', description: '', sort_order: 0 })

const showMembers = ref(false)
const membersGroup = ref<any>(null)
const memberIDs = ref<number[]>([])

const userOptions = computed(() =>
  candidates.value.map(u => ({ label: u.email ? `${u.username} (${u.email})` : u.username, value: u.id }))
)

// Packages restricted to this group — shows the admin what a group actually unlocks.
function packageNames(groupID: number) {
  return packages.value
    .filter(p => (p.user_group_ids || []).includes(groupID))
    .map(p => p.name)
    .join('、')
}

function openForm(g?: any) {
  editing.value = g || null
  Object.assign(form, g
    ? { name: g.name, description: g.description || '', sort_order: g.sort_order || 0 }
    : { name: '', description: '', sort_order: 0 })
  showForm.value = true
}

async function handleSave() {
  if (!form.name.trim()) { message.error('请填写用户组名称'); return }
  saving.value = true
  try {
    if (editing.value) await apiPut(`/api/admin/user-groups/${editing.value.id}`, form)
    else await apiPost('/api/admin/user-groups', form)
    message.success('保存成功'); showForm.value = false; editing.value = null; await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// The backend refuses with 409 when the group is some package's only
// restriction, since deleting would quietly make that package public. Only that
// case is overridable — any other error is a real failure and forcing would
// just hit it again.
async function handleDelete(g: any) {
  try {
    await apiDelete(`/api/admin/user-groups/${g.id}`)
    message.success('已删除'); await load()
  } catch (e: any) {
    if (e.status !== 409) { message.error(e.message); return }
    dialog.warning({
      title: '确认删除用户组？',
      content: e.message,
      positiveText: '仍然删除',
      negativeText: '取消',
      onPositiveClick: async () => {
        try {
          await apiDelete(`/api/admin/user-groups/${g.id}?force=1`)
          message.success('已删除'); await load()
        } catch (err: any) { message.error(err.message) }
      },
    })
  }
}

// candidates is the pool the member picker draws its options from. It must
// always contain every current member, because /api/admin/users only returns
// the 200 newest — without merging the members in, an older member would render
// as a bare id with no label. Searching adds more candidates on demand.
const candidates = ref<any[]>([])
const searching = ref(false)

function mergeCandidates(list: any[]) {
  const byID = new Map<number, any>(candidates.value.map(u => [u.id, u]))
  for (const u of list) byID.set(u.id, u)
  candidates.value = [...byID.values()]
}

async function openMembers(g: any) {
  membersGroup.value = g
  memberIDs.value = []
  candidates.value = []
  showMembers.value = true
  try {
    const [recent, members] = await Promise.all([
      apiList('/api/admin/users'),
      apiList(`/api/admin/user-groups/${g.id}/members`),
    ])
    mergeCandidates(members) // members first — they must always be selectable
    mergeCandidates(recent)
    memberIDs.value = members.map((u: any) => u.id)
  } catch (e: any) { message.error(e.message) }
}

// The user list is capped server-side, so let the admin reach the rest by
// searching instead of silently offering only the newest 200.
let searchSeq = 0
async function handleSearchUsers(q: string) {
  if (!q) return
  const seq = ++searchSeq
  searching.value = true
  try {
    const found = await apiList(`/api/admin/users?q=${encodeURIComponent(q)}`)
    if (seq === searchSeq) mergeCandidates(found) // ignore out-of-order replies
  } catch { /* keep the existing options */ } finally {
    if (seq === searchSeq) searching.value = false
  }
}

async function handleSaveMembers() {
  const g = membersGroup.value
  if (!g) return
  saving.value = true
  try {
    // One transactional call: the whole membership, applied or not at all.
    const members = await apiPut(`/api/admin/user-groups/${g.id}/members`, { user_ids: memberIDs.value })
    message.success(`已保存，共 ${members?.length ?? 0} 位成员`)
    showMembers.value = false
    await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

async function load() {
  loading.value = true
  try {
    const [g, p] = await Promise.all([
      apiList('/api/admin/user-groups'),
      apiList('/api/admin/packages').catch(() => []),
    ])
    groups.value = g; packages.value = p
  } catch (e: any) { message.error(e.message) } finally { loading.value = false }
}
onMounted(load)
</script>

<template>
  <div>
    <div class="page-head">
      <div><h2 class="page-title">套餐管理</h2><p class="page-sub">商品规格、时长阶梯、库存、权限与上下架状态</p></div>
      <div class="page-actions"><n-button type="primary" @click="openForm()">创建套餐</n-button></div>
    </div>
    <div class="resource-overview">
      <div class="resource-metric"><b>{{ packages.length }}</b><span>全部套餐</span></div>
      <div class="resource-metric success"><b>{{ packages.filter(p => p.enabled !== false).length }}</b><span>正在上架</span></div>
      <div class="resource-metric"><b>{{ packages.filter(p => p.type === 'plan').length }}</b><span>订阅计划</span></div>
      <div class="resource-metric"><b>{{ packages.filter(p => p.type === 'traffic').length }}</b><span>流量包 · 专属 {{ packages.filter(p => p.user_group_ids?.length).length }}</span></div>
    </div>
    <n-spin :show="loading">
      <div v-if="packages.length" class="card-grid">
        <div v-for="(p, idx) in packages" :key="p.id" class="list-card">
          <div class="lc-head">
            <span class="lc-title">{{ p.name || '—' }}</span>
            <n-tag v-if="p.user_group_ids?.length" type="warning" size="tiny" :bordered="false" :title="userGroupNames(p.user_group_ids)">
              专属
            </n-tag>
            <n-tag :type="p.enabled !== false ? 'success' : 'default'" size="tiny" :bordered="false">{{ p.enabled !== false ? '上架' : '下架' }}</n-tag>
          </div>
          <div class="lc-meta">
            <span class="kv"><n-tag :type="p.type === 'traffic' ? 'info' : 'success'" size="tiny" :bordered="false">{{ p.type === 'traffic' ? '流量' : '计划' }}</n-tag></span>
            <span class="kv">积分 <b>{{ p.price_points }}</b></span>
            <span class="kv">库存 <b>{{ p.stock < 0 ? '不限' : p.stock }}</b></span>
            <span class="kv">订阅 <b>{{ p.subscribers || 0 }}</b></span>
          </div>
          <div class="lc-meta" style="color:var(--text-2);">
            <span class="kv">{{ fmtTotal(p.traffic_bytes) }}</span>
            <span v-if="p.duration_days" class="kv">{{ p.duration_days }}天</span>
            <span v-if="p.type === 'plan' && p.queue_key" class="kv">续期组 <b>{{ p.queue_key }}</b></span>
          </div>
          <!-- 多时长套餐：把每档时长的价格摊开，免得只看到默认那档 -->
          <div v-if="p.options?.length > 1" class="lc-opts">
            <span v-for="(o, i) in p.options" :key="o.days" class="opt-chip" :class="{ def: i === 0 }">
              {{ o.days }}天 · {{ o.price_points }}分 · {{ fmtTotal(o.traffic_bytes) }}
            </span>
          </div>
          <div v-if="p.user_group_ids?.length" class="lc-meta" style="color:var(--text-3);">
            <span class="kv">仅限 <b>{{ userGroupNames(p.user_group_ids) }}</b> 购买</span>
          </div>
          <div v-if="p.description" class="lc-meta" style="color:var(--text-3);">{{ p.description }}</div>
          <div v-if="p.highlights?.length" class="lc-meta" style="color:var(--text-3);gap:4px 10px;">
            <span v-for="(h, i) in p.highlights" :key="i" class="kv">✓ {{ h }}</span>
          </div>
          <div class="lc-foot" style="flex-wrap:wrap;">
            <n-button size="tiny" :disabled="idx === 0 || reordering" title="前移（在商城/列表更靠前）" @click="movePackage(idx, -1)">←</n-button>
            <n-button size="tiny" :disabled="idx === packages.length - 1 || reordering" title="后移" @click="movePackage(idx, 1)">→</n-button>
            <n-button size="tiny" @click="openForm(p)">编辑</n-button>
            <n-button v-if="p.enabled !== false" size="tiny" type="warning" @click="handleRetire(p)">下架</n-button>
            <n-button v-else size="tiny" type="success" @click="handleEnable(p.id)">上架</n-button>
            <n-button size="tiny" type="error" @click="handleDelete(p)">删除</n-button>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="暂无套餐" style="padding:40px 0;" />
    </n-spin>

    <n-modal v-model:show="showForm" preset="card" :title="editing ? '编辑套餐' : '创建套餐'" style="max-width:520px;">
      <n-form label-placement="left" label-width="100">
        <n-form-item label="名称"><n-input v-model:value="form.name" /></n-form-item>
        <n-form-item label="类型">
          <n-select v-model:value="form.type" :options="[{label:'流量包',value:'traffic'},{label:'订阅计划',value:'plan'}]" />
        </n-form-item>
        <n-form-item v-if="form.type==='plan'" label="续期组">
          <div style="width:100%;">
            <n-input v-model:value="form.queue_key" placeholder="留空 = 仅与当前套餐续期排队" />
            <div style="margin-top:4px;font-size:12px;color:var(--text-3);line-height:1.5;">
              两个套餐填写相同续期组时互相排队；不同组立即生效。仅限字母、数字、点、下划线和短横线。
            </div>
          </div>
        </n-form-item>
        <n-form-item label="描述"><n-input v-model:value="form.description" type="textarea" :autosize="{ minRows: 1, maxRows: 3 }" placeholder="套餐一句话说明" /></n-form-item>
        <n-form-item label="亮点">
          <div style="width:100%;">
            <n-dynamic-input v-model:value="form.highlights" :max="8" placeholder="一条卖点，如：全球 50+ 节点 / 不限速 / 7×24 客服" />
            <div style="margin-top:4px;font-size:12px;color:var(--text-3);line-height:1.5;">
              一行一个卖点，商城里会以清单形式展示，最多 8 条。留空则不显示。
            </div>
          </div>
        </n-form-item>
        <!-- 订阅计划：时长可以有多档，用户在商城里自己挑；流量按档单独给，
             因为一份套餐的流量在有效期内不会按月重置，长档不加量就是更亏。 -->
        <n-form-item v-if="form.type==='plan'" label="时长/价格">
          <div style="width:100%;">
            <div class="opt-head">
              <span>天数</span><span>流量 (GB)</span><span>积分</span><span />
            </div>
            <div v-for="(o, i) in form.options" :key="i" class="opt-row">
              <n-input-number v-model:value="o.days" :min="1" :show-button="false" placeholder="30" />
              <n-input-number v-model:value="o.traffic_gb" :min="0.01" :show-button="false" placeholder="100" />
              <n-input-number v-model:value="o.price" :min="0" :show-button="false" placeholder="100" />
              <n-button quaternary size="small" :disabled="form.options.length <= 1"
                        title="删除该档" @click="removeOption(i)">✕</n-button>
            </div>
            <div class="opt-actions">
              <n-button size="tiny" dashed :disabled="form.options.length >= 8" @click="addOption()">+ 添加时长</n-button>
              <span class="opt-quick">快捷：</span>
              <n-button v-for="d in [7, 30, 90, 180, 365]" :key="d" size="tiny" quaternary
                        :disabled="form.options.length >= 8 || form.options.some(o => o.days === d)"
                        @click="addOption(d)">{{ d }}天</n-button>
            </div>
            <div class="opt-tip">
              第一档是默认档：商城卡片默认选中它，管理员分配套餐不指定时长时也用它。
              最多 8 档，天数不能重复。只留一档就是普通的单时长套餐。
              快捷添加会按第一档的单价自动折算流量和积分，可再手动改。
            </div>
          </div>
        </n-form-item>
        <template v-else>
          <n-form-item label="流量 (GB)"><n-input-number v-model:value="form.traffic_gb" :min="0.01" style="width:100%;" /></n-form-item>
          <n-form-item label="天数"><n-input-number v-model:value="form.days" :min="0" style="width:100%;" /></n-form-item>
          <n-form-item label="积分"><n-input-number v-model:value="form.price" :min="0" style="width:100%;" /></n-form-item>
        </template>
        <n-form-item label="库存（-1不限）"><n-input-number v-model:value="form.stock" :min="-1" style="width:100%;" /></n-form-item>
        <n-form-item v-if="form.type==='plan'" label="节点分组">
          <n-select v-model:value="form.group_ids" :options="groupOptions" multiple placeholder="买了这个套餐，能用哪些节点" />
        </n-form-item>
        <n-form-item label="可购买用户组">
          <div style="width:100%;">
            <n-select
              v-model:value="form.user_group_ids"
              :options="userGroupOptions"
              multiple
              clearable
              placeholder="留空 = 所有人都能买"
            />
            <div style="margin-top:4px;font-size:12px;color:var(--text-3);line-height:1.5;">
              {{ form.user_group_ids.length
                ? '专属套餐：只有所选用户组的成员能看到并购买。'
                : '公开套餐：所有用户都能购买。选择用户组后即变为专属。' }}
            </div>
          </div>
        </n-form-item>
      </n-form>
      <n-button type="primary" block :loading="saving" @click="handleSave">保存</n-button>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import {
  NSpin, NButton, NModal, NForm, NFormItem, NInput, NInputNumber,
  NSelect, NTag, NEmpty, NDynamicInput, useMessage, useDialog
} from 'naive-ui'
import { apiList, apiPost, apiPut, apiDelete } from '@/api'
import { fmtTotal, yuan } from '@/utils/format'

const message = useMessage()
const dialog = useDialog()
const packages = ref<any[]>([])
const groups = ref<any[]>([])
const userGroups = ref<any[]>([])
const loading = ref(false)
const saving = ref(false)
const reordering = ref(false)
const showForm = ref(false)
const editing = ref<any>(null)
type OptRow = { days: number | null; traffic_gb: number | null; price: number | null }
const form = reactive({ name: '', type: 'traffic', queue_key: '', description: '', highlights: [] as string[], traffic_gb: 100, days: 30, price: 100, stock: -1, options: [] as OptRow[], group_ids: [] as number[], user_group_ids: [] as number[] })

const GB = 1024 * 1024 * 1024

// 订阅计划的时长档位。表单里始终至少有一档：单档保存成普通套餐（不写
// options），多档才写成可选时长。
function addOption(days?: number) {
  const first = form.options[0]
  if (days && first?.days) {
    // 按第一档的单价折算，长档通常还要打折——这里只给个起点，管理员再改。
    const k = days / first.days
    form.options.push({
      days,
      traffic_gb: Math.round((first.traffic_gb || 0) * k * 100) / 100,
      price: Math.round((first.price || 0) * k),
    })
  } else {
    form.options.push({ days: days || null, traffic_gb: first?.traffic_gb ?? 100, price: first?.price ?? 0 })
  }
}
function removeOption(i: number) {
  if (form.options.length > 1) form.options.splice(i, 1)
}

// groups = node groups (which nodes a plan grants); userGroups = who may buy it.
const groupOptions = computed(() => groups.value.map(g => ({ label: g.name, value: g.id })))
const userGroupOptions = computed(() => userGroups.value.map(g => ({ label: g.name, value: g.id })))

function userGroupNames(ids: number[]) {
  return ids
    .map(id => userGroups.value.find(g => g.id === id)?.name)
    .filter(Boolean)
    .join('、')
}

// 已保存的套餐若没有多档，就用它自身的天数/流量/积分当作第一档，这样把类型
// 切成「订阅计划」时表格里是原来的数，而不是空的。
function optRowsOf(pkg?: any): OptRow[] {
  const opts = Array.isArray(pkg?.options) ? pkg.options : []
  if (opts.length) return opts.map((o: any) => ({ days: o.days, traffic_gb: (o.traffic_bytes || 0) / GB, price: o.price_points || 0 }))
  return [{ days: pkg?.duration_days || 30, traffic_gb: pkg ? (pkg.traffic_bytes || 0) / GB : 100, price: pkg?.price_points ?? 100 }]
}

function openForm(pkg?: any) {
  editing.value = pkg || null
  if (pkg) {
    Object.assign(form, {
      name: pkg.name, type: pkg.type, queue_key: pkg.queue_key || '', description: pkg.description || '',
      highlights: Array.isArray(pkg.highlights) ? [...pkg.highlights] : [],
      traffic_gb: (pkg.traffic_bytes || 0) / GB, days: pkg.duration_days || 0,
      price: pkg.price_points || 0, stock: pkg.stock ?? -1,
      options: optRowsOf(pkg),
      group_ids: pkg.group_ids || [], user_group_ids: pkg.user_group_ids || [],
    })
  } else {
    Object.assign(form, { name: '', type: 'traffic', queue_key: '', description: '', highlights: [], traffic_gb: 100, days: 30, price: 100, stock: -1, options: optRowsOf(), group_ids: [], user_group_ids: [] })
  }
  showForm.value = true
}

async function handleSave() {
  saving.value = true
  try {
    const { traffic_gb, days, price, options, ...rest } = form
    const isPlan = form.type === 'plan'
    // 计划的价格/流量/天数都来自档位表；单档不写 options，存成普通套餐。
    const opts = isPlan
      ? options.map(o => ({ days: o.days || 0, price_points: o.price || 0, traffic_bytes: Math.round((o.traffic_gb || 0) * GB) }))
      : []
    const first = opts[0]
    const body = {
      ...rest,
      queue_key: isPlan ? form.queue_key : '',
      options: opts.length > 1 ? opts : [],
      traffic_bytes: isPlan ? (first?.traffic_bytes || 0) : Math.round(traffic_gb * GB),
      duration_days: isPlan ? (first?.days || 0) : days,
      price_points: isPlan ? (first?.price_points || 0) : price,
    }
    if (editing.value) await apiPut(`/api/admin/packages/${editing.value.id}`, body)
    else await apiPost('/api/admin/packages', body)
    message.success('保存成功'); showForm.value = false; editing.value = null; await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// 调整套餐在商城/列表中的展示顺序：把第 idx 个套餐前移(-1)/后移(+1)一位，
// 乐观更新本地数组后把完整 id 顺序提交给后端（写入 sort_order）。
async function movePackage(idx: number, dir: -1 | 1) {
  const target = idx + dir
  if (target < 0 || target >= packages.value.length || reordering.value) return
  const arr = [...packages.value]
  const [moved] = arr.splice(idx, 1)
  arr.splice(target, 0, moved)
  packages.value = arr
  reordering.value = true
  try {
    await apiPost('/api/admin/packages/reorder', { ids: arr.map(p => p.id) })
  } catch (e: any) {
    message.error(e.message || '排序失败'); await load()
  } finally { reordering.value = false }
}

// 下架 refunds every holder and clears their plan — irreversible and moves points,
// so it must be confirmed with the impact spelled out.
function handleRetire(p: any) {
  const cnt = p.subscribers || 0
  dialog.warning({
    title: '确认下架套餐',
    content: cnt > 0
      ? `「${p.name}」当前有 ${cnt} 位用户持有。下架会按剩余流量/时间比例给他们退款并清空该套餐，操作不可撤销。确定继续？`
      : `确定下架「${p.name}」？下架后不可购买（如仍有持有者会被退款并清空）。`,
    positiveText: '下架', negativeText: '取消',
    onPositiveClick: async () => {
      try { await apiPost(`/api/admin/packages/${p.id}/retire`); message.success('已下架'); await load() }
      catch (e: any) { message.error(e.message) }
    },
  })
}
async function handleEnable(id: number) {
  try { await apiPost(`/api/admin/packages/${id}/enable`); message.success('已上架'); await load() } catch (e: any) { message.error(e.message) }
}
function handleDelete(p: any) {
  dialog.warning({
    title: '确认删除套餐',
    content: `确定永久删除「${p.name}」？该操作不可撤销。若仍有用户持有，请先下架。`,
    positiveText: '删除', negativeText: '取消',
    onPositiveClick: async () => {
      try { await apiDelete(`/api/admin/packages/${p.id}`); message.success('已删除'); await load() }
      catch (e: any) { message.error(e.message) }
    },
  })
}

async function load() {
  loading.value = true
  try {
    const [pkgs, g, ug] = await Promise.all([
      apiList('/api/admin/packages'),
      apiList('/api/admin/node-groups').catch(() => []),
      apiList('/api/admin/user-groups').catch(() => []),
    ])
    packages.value = pkgs; groups.value = g; userGroups.value = ug
  } catch (e: any) { message.error('加载失败：' + (e?.message || '请稍后重试')) } finally { loading.value = false }
}
onMounted(load)
</script>

<style scoped>
/* 时长档位表：三列等宽 + 删除按钮，列头只写一次 */
.opt-head, .opt-row {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr 28px;
  gap: 6px;
  align-items: center;
}
.opt-head { font-size: 12px; color: var(--text-3); margin-bottom: 4px; }
.opt-row { margin-bottom: 6px; }
.opt-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-top: 2px; }
.opt-quick { font-size: 12px; color: var(--text-3); margin-left: 4px; }
.opt-tip { margin-top: 6px; font-size: 12px; color: var(--text-3); line-height: 1.5; }

.lc-opts { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
.opt-chip {
  font-size: 11px;
  color: var(--text-2);
  background: var(--bg-2, #f6f6f6);
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 2px 8px;
  white-space: nowrap;
}
.opt-chip.def { color: var(--text); font-weight: 600; }
</style>

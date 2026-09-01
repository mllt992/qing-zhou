<template>
  <div>
    <div class="au-head">
      <div>
        <h2 class="page-title" style="margin-bottom:4px;">用户管理</h2>
        <p class="page-sub">共 {{ users.length }} 位用户，{{ onlineCount }} 位在线</p>
      </div>
      <div class="au-actions">
        <n-button size="small" quaternary :loading="loading" @click="load">
          <template #icon><n-icon><RefreshOutline /></n-icon></template>
          刷新
        </n-button>
        <n-button size="small" type="primary" @click="openCreate">
          <template #icon><n-icon><PersonAddOutline /></n-icon></template>
          创建用户
        </n-button>
      </div>
    </div>

    <!-- 概览：一眼看清这批用户的构成 -->
    <div class="stat-strip">
      <button v-for="s in stats" :key="s.key" class="ss-item" :class="{ on: filter === s.key }"
              type="button" @click="filter = s.key">
        <span class="ss-val" :style="s.color ? { color: s.color } : {}">{{ s.value }}</span>
        <span class="ss-label">{{ s.label }}</span>
      </button>
    </div>

    <div class="page-toolbar">
      <n-input v-model:value="search" placeholder="搜索用户名 / 邮箱 / 备注" style="width:240px;max-width:60%;" clearable>
        <template #prefix><n-icon><SearchOutline /></n-icon></template>
      </n-input>
      <n-select v-model:value="sortBy" :options="sortOptions" size="small" style="width:132px;" />
      <span class="spacer" />
      <span class="au-count">{{ filtered.length }} / {{ users.length }}</span>
    </div>

    <n-spin :show="loading">
      <div v-if="filtered.length" class="card-grid">
        <div v-for="u in filtered" :key="u.id" class="user-card">
          <!-- 身份 -->
          <div class="uc-head">
            <span class="uc-avatar" :style="avatarStyle(u.username)">{{ initial(u.username) }}</span>
            <div class="uc-id">
              <div class="uc-name-row">
                <span class="uc-name" :title="u.username">{{ u.username }}</span>
                <span v-if="u.online" class="dot-live" title="在线" />
                <n-tag v-if="u.status === 'banned'" type="error" size="tiny" :bordered="false">封禁</n-tag>
                <n-tag v-if="u.role === 'admin'" type="warning" size="tiny" :bordered="false">管理员</n-tag>
              </div>
              <div class="uc-sub" :title="u.email || ''">
                {{ u.email || '未绑定邮箱' }} · {{ u.online ? '在线' : timeAgo(u.last_online_at) }}
              </div>
              <!-- 管理员备注：一眼看出这号是谁的。空备注不占位，卡片高度不变 -->
              <div v-if="u.remark" class="uc-remark" :title="u.remark">{{ u.remark }}</div>
            </div>
          </div>

          <!-- 流量：按可用份汇总，排队 / 已过期的份不计入（后端 traffic 口径） -->
          <div class="uc-block">
            <div class="uc-row">
              <span class="uc-k">流量</span>
              <span class="uc-v">{{ trafficMain(u) }}</span>
              <span v-if="meteredOf(u)" class="uc-pct" :style="{ color: barColor(usedPctOf(u)) }">{{ usedPctOf(u) }}%</span>
            </div>
            <div class="bar">
              <div class="bar-fill" :style="{ width: barWidth(u), background: barColor(usedPctOf(u)) }" />
            </div>
            <!-- 卡片高度要齐，这行超出就截断；title 让完整内容仍然可得 -->
            <div class="uc-note" :title="trafficNote(u)">{{ trafficNote(u) }}</div>
          </div>

          <!-- 套餐：卡片只报「有几份、在用哪几个、什么时候要续」，明细在套餐面板 -->
          <button class="uc-block uc-plans" type="button" @click="openPlans(u)">
            <div class="uc-row">
              <span class="uc-k">套餐</span>
              <span class="uc-chips">
                <template v-if="u.plan_summary">
                  <i v-if="u.plan_summary.active" class="chip ok">生效 {{ u.plan_summary.active }}</i>
                  <i v-if="u.plan_summary.queued" class="chip q">排队 {{ u.plan_summary.queued }}</i>
                  <i v-if="u.plan_summary.finished" class="chip fin">已结束 {{ u.plan_summary.finished }}</i>
                  <i v-if="!hasAnyPlan(u)" class="chip none">暂无套餐</i>
                </template>
                <i v-else class="chip none">—</i>
              </span>
              <span class="uc-arrow" aria-hidden="true">›</span>
            </div>
            <div class="uc-note" :title="planNote(u)">{{ planNote(u) }}</div>
          </button>

          <div class="uc-meta">
            <span class="kv">积分 <b>{{ u.points }}</b></span>
            <span class="kv" :title="subFetchTitle(u)">订阅 <b>{{ subFetchText(u) }}</b></span>
            <span v-if="u.group_ids?.length" class="kv">用户组 <b>{{ groupNames(u.group_ids) }}</b></span>
          </div>

          <div class="uc-foot">
            <n-button size="tiny" type="primary" secondary @click="openPlans(u)">套餐</n-button>
            <n-button size="tiny" @click="openEdit(u)">编辑</n-button>
            <n-button size="tiny" @click="openRecharge(u)">充值</n-button>
            <n-button size="tiny" @click="openOrders(u)">订单</n-button>
            <span class="spacer" />
            <n-dropdown trigger="click" :options="moreOptions" @select="(k: string) => onMore(k, u)">
              <n-button size="tiny" quaternary :loading="resettingCreds === u.id">⋯</n-button>
            </n-dropdown>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" :description="emptyText" style="padding:40px 0;" />
    </n-spin>

    <!-- ===== 套餐面板：查看 / 聚合 / 移除 / 分配，一个用户的套餐都在这里 ===== -->
    <n-modal v-model:show="showPlans" preset="card" style="max-width:760px;"
             :title="'套餐管理 · ' + (plansUser?.username || '')">
      <n-spin :show="loadingPlans">
        <!-- 口径与用户端控制台一致：只统计当前可用的份 -->
        <div class="pm-summary">
          <div class="pm-stat">
            <span class="pm-label">可用流量</span>
            <b class="pm-val">{{ plansUser ? trafficMain(plansUser) : '—' }}</b>
            <i class="pm-hint">{{ plansUser ? trafficNote(plansUser) : '' }}</i>
          </div>
          <div class="pm-stat">
            <span class="pm-label">份数</span>
            <b class="pm-val">{{ counts.active }} <em>生效</em></b>
            <i class="pm-hint">排队 {{ counts.queued }} · 已结束 {{ counts.finished }}</i>
          </div>
          <div class="pm-stat">
            <span class="pm-label">累计用量</span>
            <b class="pm-val">{{ fmtBytes(counts.totalUsed) }}</b>
            <i class="pm-hint">含已结束的份与流量包{{ freeUsedText }}</i>
          </div>
        </div>

        <div class="pm-bar">
          <n-radio-group v-model:value="planFilter" size="small">
            <n-radio-button value="all">全部</n-radio-button>
            <n-radio-button value="active">生效中</n-radio-button>
            <n-radio-button value="queued">排队中</n-radio-button>
            <n-radio-button value="finished">已结束</n-radio-button>
          </n-radio-group>
          <span class="spacer" />
          <n-checkbox v-model:checked="aggregate" size="small">聚合同套餐</n-checkbox>
        </div>

        <!-- 聚合视图：同一套餐的多份合成一行，展开看每一份 -->
        <template v-if="aggregate">
          <div v-for="g in planGroups" :key="g.key" class="grp">
            <button class="grp-head" type="button" @click="toggleGroup(g.key)">
              <span class="chev" :class="{ open: openGroups.has(g.key) }" aria-hidden="true">›</span>
              <span class="grp-name" :title="g.name">{{ g.name }}</span>
              <n-tag v-if="g.kind === 'pool'" size="tiny" type="info" :bordered="false">流量包</n-tag>
              <n-tag v-else-if="g.items.length > 1" size="tiny" :bordered="false">{{ g.items.length }} 份</n-tag>
              <span class="grp-chips">
                <i v-if="g.active" class="chip ok">生效 {{ g.active }}</i>
                <i v-if="g.queued" class="chip q">排队 {{ g.queued }}</i>
                <i v-if="g.finished" class="chip fin">已结束 {{ g.finished }}</i>
              </span>
              <span class="grp-num">{{ groupAvailText(g) }}</span>
            </button>
            <div class="bar slim">
              <div class="bar-fill" :style="{ width: groupBarWidth(g), background: barColor(groupPct(g)) }" />
            </div>
            <div class="grp-note" :title="groupNote(g)">{{ groupNote(g) }}</div>

            <div v-if="openGroups.has(g.key)" class="grp-items">
              <plan-item v-for="p in g.items" :key="p.id" :plan="p" :removing="removingId === p.id"
                         @remove="removePlan(p)" @adjust="openAdjust(p)" />
            </div>
          </div>
        </template>

        <!-- 平铺视图：每一份独立一行 -->
        <template v-else>
          <div class="flat">
            <plan-item v-for="p in visiblePlans" :key="p.id" :plan="p" :removing="removingId === p.id"
                       @remove="removePlan(p)" @adjust="openAdjust(p)" />
          </div>
        </template>

        <n-empty v-if="!loadingPlans && !visiblePlans.length"
                 :description="userPlans.length ? '没有符合筛选的套餐' : '该用户暂无套餐'" size="small" style="padding:24px 0;" />

        <!-- 分配：不扣积分的手动开通，就放在套餐列表下面，看完再决定 -->
        <div class="pm-assign">
          <div class="pm-assign-title">分配套餐（不扣积分）</div>
          <div class="pm-assign-row">
            <n-select v-model:value="assignPkgId" :options="pkgOptions" placeholder="选择套餐" filterable style="flex:1;" />
            <!-- 天数只对订阅计划有意义：流量包加的是共享池，没有按份倒计时。 -->
            <n-input-number v-if="assignIsPlan" v-model:value="assignDays" :min="1" :max="assignDaysMax" :precision="0"
                            :show-button="false" placeholder="天数" style="width:96px;flex:none;" />
            <n-button type="primary" :loading="saving" :disabled="!assignPkgId" @click="handleAssign">
              {{ assignWillQueue ? '分配并排队' : '分配' }}
            </n-button>
          </div>
          <div v-if="assignDayChips.length" class="pm-assign-chips">
            <span class="pm-assign-chips-label">快捷</span>
            <n-button v-for="d in assignDayChips" :key="d" size="tiny" quaternary
                      :type="assignDays === d ? 'primary' : 'default'" @click="assignDays = d">{{ d }} 天</n-button>
          </div>
          <div v-if="assignIsPlan" class="pm-assign-tip">可填 1–3650 天。命中套餐档位用该档流量，自定义天数按默认档流量开通。</div>
          <div v-if="assignWillQueue" class="pm-assign-hint">
            该用户已有同一续期组的套餐在生效，新的一份会排队，等当前份用完或到期后自动启用，届时才开始计算有效期。
          </div>
          <!-- 管理员账号也能分配：它在 sing-box 侧就是一个普通身份，自己拿来当订阅用很常见 -->
          <div v-if="plansUser?.role === 'admin' && !assignWillQueue" class="pm-assign-hint">
            管理员账号同样可以持有套餐，分配后即可用自己的订阅链接。
          </div>
        </div>
      </n-spin>
    </n-modal>

    <!-- 创建用户 -->
    <n-modal v-model:show="showCreate" preset="card" title="创建用户" style="max-width:400px;">
      <n-form label-placement="left" label-width="60">
        <n-form-item label="用户名"><n-input v-model:value="newUser.username" /></n-form-item>
        <n-form-item label="邮箱"><n-input v-model:value="newUser.email" /></n-form-item>
        <n-form-item label="密码"><n-input v-model:value="newUser.password" type="password" /></n-form-item>
        <n-form-item label="积分"><n-input-number v-model:value="newUser.points" :min="0" style="width:100%;" /></n-form-item>
        <n-form-item label="用户组">
          <n-select v-model:value="newUser.group_ids" :options="userGroupOptions" multiple clearable placeholder="留空 = 只能买公开套餐" />
        </n-form-item>
        <n-form-item label="备注">
          <div style="width:100%;">
            <n-input v-model:value="newUser.remark" type="textarea" :autosize="{ minRows: 1, maxRows: 3 }"
                     maxlength="200" show-count placeholder="仅管理员可见，如「张三 · 公司账号」" />
            <div class="form-hint">只在管理后台显示，用户本人看不到，也不会写进订阅或节点配置。</div>
          </div>
        </n-form-item>
      </n-form>
      <n-button type="primary" block :loading="saving" @click="handleCreate">创建</n-button>
    </n-modal>

    <!-- 编辑用户 -->
    <n-modal v-model:show="showEdit" preset="card" title="编辑用户" style="max-width:500px;">
      <n-form v-if="editUser" label-placement="left" label-width="80">
        <n-form-item label="用户名"><n-input :value="editUser.username" disabled /></n-form-item>
        <n-form-item label="邮箱">
          <div style="width:100%;">
            <n-input v-model:value="editEmail" placeholder="留空 = 解除绑定" />
            <div class="form-hint">
              管理员改邮箱直接生效、直接算已验证，不给对方发验证信 ——
              用户自己换绑要点新邮箱里的链接，收不到信的人正是来找你改的那个。留空则解除绑定。
            </div>
          </div>
        </n-form-item>
        <n-form-item label="备注">
          <div style="width:100%;">
            <n-input v-model:value="editRemark" type="textarea" :autosize="{ minRows: 1, maxRows: 3 }"
                     maxlength="200" show-count placeholder="仅管理员可见，如「张三 · 公司账号」" />
            <div class="form-hint">只在管理后台显示，用户本人看不到，也不会写进订阅或节点配置。留空即清除备注。</div>
          </div>
        </n-form-item>
        <n-form-item label="手动额度">
          <div style="width:100%;">
            <n-switch v-model:value="manualEnabled" />
            <div class="form-hint">
              管理员赠送的通用流量，作为一个独立计量的额度桶，作用于该用户「免费分组 + 已购套餐分组」的节点。需要指定具体节点分组请改用「套餐 → 分配套餐」。
            </div>
          </div>
        </n-form-item>
        <template v-if="manualEnabled">
          <n-form-item label="流量 (GB)"><n-input-number v-model:value="editTrafficGB" :min="0.01" style="width:100%;" /></n-form-item>
          <n-form-item label="到期时间"><n-input v-model:value="editExpiry" :input-props="{ type: 'datetime-local' }" style="width:100%;" /></n-form-item>
        </template>
        <n-form-item label="用户组">
          <div style="width:100%;">
            <n-select v-model:value="editGroupIDs" :options="userGroupOptions" multiple clearable placeholder="留空 = 只能买公开套餐" />
            <div class="form-hint">决定该用户能买哪些专属套餐。移出用户组不影响其已购买的套餐。</div>
          </div>
        </n-form-item>
        <n-form-item label="封禁"><n-switch v-model:value="editBanned" /></n-form-item>
        <n-form-item label="重置密码"><n-input v-model:value="resetPw" type="password" placeholder="留空不重置" /></n-form-item>
        <n-form-item label="重置流量">
          <div style="width:100%;">
            <n-switch v-model:value="resetTraffic" />
            <div class="form-hint">把该用户所有份的已用流量清零，额度与到期时间不变。</div>
          </div>
        </n-form-item>
      </n-form>
      <n-button type="primary" block :loading="saving" @click="handleSave">保存</n-button>
    </n-modal>

    <!-- 积分充值 -->
    <n-modal v-model:show="showRecharge" preset="card" title="积分充值" style="max-width:400px;">
      <p class="modal-sub">用户：{{ rechargeUser?.username }}（当前 {{ rechargeUser?.points }} 积分）</p>
      <n-form label-placement="left" label-width="60">
        <n-form-item label="积分"><n-input-number v-model:value="rechargeAmount" style="width:100%;" /></n-form-item>
        <n-form-item label="说明"><n-input v-model:value="rechargeNote" placeholder="正数充值，负数扣除" /></n-form-item>
      </n-form>
      <n-button type="primary" block :loading="saving" @click="handleRecharge">确认</n-button>
    </n-modal>

    <!-- 按份调整流量：给某一份额度加减 GB，不是退款也不是新开一份 -->
    <n-modal v-model:show="showAdjust" preset="card" title="调整流量" style="max-width:420px;">
      <p class="modal-sub">
        {{ plansUser?.username }} · {{ adjustPlan?.name || '份' }}
        <template v-if="adjustPlan">
          （当前 {{ adjustAmountText }}）
        </template>
      </p>
      <n-form label-placement="left" label-width="80">
        <n-form-item label="调整 (GB)">
          <n-input-number v-model:value="adjustGB" :show-button="true" style="width:100%;" />
        </n-form-item>
      </n-form>
      <p class="form-hint" style="margin:-4px 0 12px;">
        正数增加、负数扣减。扣减不会低于已用流量。排队中尚未生效的份也可以改额度。
        这不是退款：积分与订单都不动。
      </p>
      <n-button type="primary" block :loading="saving" :disabled="!adjustGB" @click="handleAdjust">确认</n-button>
    </n-modal>

    <!-- 订单历史 -->
    <n-modal v-model:show="showOrders" preset="card" title="用户订单" style="max-width:700px;">
      <p class="modal-sub">用户：{{ ordersUser?.username }}</p>
      <n-spin :show="loadingOrders">
        <div v-if="userOrders.length" class="card-grid compact">
          <div v-for="o in userOrders" :key="o.id" class="list-card">
            <div class="lc-head">
              <span class="lc-title">{{ o.name || '—' }}</span>
              <n-tag :type="o.status === 'success' ? 'success' : 'warning'" size="tiny" :bordered="false">{{ o.status === 'success' ? '成功' : '已退款' }}</n-tag>
            </div>
            <div class="lc-meta">
              <span class="kv">积分 <b>{{ o.price_points }}</b></span>
              <span class="kv" v-if="o.status === 'refunded'">已退 <b style="color:var(--warn);">{{ o.refunded_points }}</b>
                <template v-if="o.refund_ratio > 0 && o.refund_ratio < 1">（{{ Math.round(o.refund_ratio * 100) }}%）</template>
              </span>
              <span class="kv">{{ fmtDateTime(o.created_at) }}</span>
            </div>
            <div class="lc-foot">
              <n-button v-if="o.status === 'success'" size="tiny" type="warning" @click="openRefund(o.id)">退款</n-button>
            </div>
          </div>
        </div>
        <n-empty v-else-if="!loadingOrders" description="暂无订单" style="padding:30px 0;" />
      </n-spin>
    </n-modal>

    <refund-dialog v-model:show="refundShow" :order-id="refundId" @done="reloadUserOrders" />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, reactive, watch, onMounted, h } from 'vue'
import {
  NSpin, NInput, NInputNumber, NButton, NModal, NForm, NFormItem, NIcon,
  NSwitch, NTag, NSelect, NEmpty, NCheckbox, NDropdown, NRadioGroup, NRadioButton,
  useMessage, useDialog
} from 'naive-ui'
import { RefreshOutline, PersonAddOutline, SearchOutline } from '@vicons/ionicons5'
import { apiList, apiPost, apiPut, apiDelete } from '@/api'
import { fmtBytes, fmtDate, fmtDateTime, timeAgo, daysLeft, pct, toLocalDatetimeInput } from '@/utils/format'
import { planStatusMeta, planSortKey } from '@/utils/plan'
import RefundDialog from '@/components/RefundDialog.vue'
import PlanItem from '@/components/AdminPlanItem.vue'

const message = useMessage()
const dialog = useDialog()
const users = ref<any[]>([])
const loading = ref(false)
const saving = ref(false)
const search = ref('')
const filter = ref('all')
const sortBy = ref('default')

const sortOptions = [
  { label: '默认排序', value: 'default' },
  { label: '最后在线', value: 'online' },
  { label: '最后拉取订阅', value: 'subfetch' },
  { label: '用量最高', value: 'usage' },
  { label: '最近到期', value: 'expiry' },
  { label: '积分最高', value: 'points' },
]

const onlineCount = computed(() => users.value.filter((u: any) => u.online).length)

const subClientLabels: Record<string, string> = {
  browser: '浏览器', mihomo: 'Mihomo', clash: 'Clash', stash: 'Stash',
  'sing-box': 'sing-box', surge: 'Surge', shadowrocket: 'Shadowrocket',
  v2rayn: 'v2rayN', curl: 'curl', unknown: '未知客户端',
}
const subFormatLabels: Record<string, string> = {
  info: '信息页', clash: 'Clash', singbox: 'sing-box', surge: 'Surge', base64: '通用',
}
function subFetchText(u: any) {
  if (!u.sub_last_fetched_at) return '从未拉取'
  return timeAgo(u.sub_last_fetched_at)
}
function subFetchTitle(u: any) {
  if (!u.sub_last_fetched_at) return '尚未成功获取过订阅'
  const client = subClientLabels[u.sub_last_client] || '未知客户端'
  const format = subFormatLabels[u.sub_last_format] || u.sub_last_format || '未知格式'
  return `${fmtDateTime(u.sub_last_fetched_at)} · ${client} · ${format}（记录最多每小时更新一次）`
}

// ---- 流量口径 ----
// 后端的 traffic 只统计「当前可用」的份：排队中的份还不能用，已过期的份订阅里
// 已经不发了，把它们的额度算进总量就是在报一个用户花不掉的数字。旧卡片读的
// used / traffic_limit 是 users.* 那份朴素求和，正是这么错的。
function trafficOf(u: any) { return u?.traffic || null }
function meteredOf(u: any) { return (trafficOf(u)?.total || 0) > 0 }
function usedPctOf(u: any) {
  const t = trafficOf(u)
  return t ? pct(t.used, t.total) : 0
}
function trafficMain(u: any) {
  const t = trafficOf(u)
  if (!t) return '—'
  if (meteredOf(u)) return `${fmtBytes(t.used)} / ${fmtBytes(t.total)}`
  return '无可用额度'
}
function trafficNote(u: any) {
  const t = trafficOf(u)
  if (!t) return '流量数据读取失败'
  const parts: string[] = []
  if (meteredOf(u)) parts.push(`剩余 ${fmtBytes(t.remaining)}`)
  if (t.free_used > 0) parts.push(`免费分组 ${fmtBytes(t.free_used)}`)
  const pool = u.plan_summary?.pool_limit || 0
  if (pool > 0) parts.push(`含流量包 ${fmtBytes(pool - (u.plan_summary.pool_used || 0))}`)
  return parts.join(' · ') || '暂无可用额度'
}
function barWidth(u: any) {
  if (meteredOf(u)) return Math.min(usedPctOf(u), 100) + '%'
  return '0%'
}
function barColor(p: number) {
  return p > 90 ? '#c2685c' : p > 70 ? '#bf9540' : '#6f8f76'
}

// ---- 套餐摘要 ----
function hasAnyPlan(u: any) {
  const s = u.plan_summary
  return !!s && (s.active || s.queued || s.finished)
}
function planNote(u: any) {
  const s = u.plan_summary
  if (!s) return '点击查看套餐明细'
  if (!hasAnyPlan(u)) return '点击分配一个套餐'
  if (!s.active) return '没有生效中的套餐，点击处理'
  const names = activeNamesText(s)
  if (!s.next_expiry_at) return names ? `${names} · 均不过期` : '均不过期'
  const d = daysLeft(s.next_expiry_at)
  const when = `${s.active > 1 ? '最近 ' : ''}${fmtDate(s.next_expiry_at)} 到期（剩 ${Math.max(d ?? 0, 0)} 天）`
  return names ? `${names} · ${when}` : when
}
function activeNamesText(s: any) {
  const counts = new Map<string, number>()
  for (const n of (s.active_names || [])) counts.set(n, (counts.get(n) || 0) + 1)
  const parts = [...counts].map(([n, c]) => (c > 1 ? `${n} ×${c}` : n))
  if (!parts.length) return ''
  return parts.slice(0, 2).join('、') + (parts.length > 2 ? ` +${parts.length - 2}` : '')
}
// 管理员现在也能持有套餐，但很多站点的 admin 只用来管理、从不走代理：无条件
// 把它算进「无生效套餐」会留下一个永远清不掉的待办。只有当它确实拿过套餐
// （说明这个号在当订阅用）时才催办。
function needsPlan(u: any) {
  if (u.plan_summary?.active) return false
  return u.role !== 'admin' || hasAnyPlan(u)
}
function expiringSoon(u: any) {
  const ts = u.plan_summary?.next_expiry_at
  if (!ts) return false
  const d = daysLeft(ts)
  return d !== null && d <= 7
}

// ---- 概览 / 筛选 ----
const stats = computed(() => [
  { key: 'all', label: '全部用户', value: users.value.length, color: '' },
  { key: 'online', label: '在线', value: onlineCount.value, color: '#6f8f76' },
  { key: 'unfetched', label: '从未拉取订阅', value: users.value.filter((u: any) => !u.sub_last_fetched_at).length, color: '#767676' },
  { key: 'noplan', label: '无生效套餐', value: users.value.filter(needsPlan).length, color: '#767676' },
  { key: 'expiring', label: '7 天内到期', value: users.value.filter(expiringSoon).length, color: '#bf9540' },
  { key: 'banned', label: '已封禁', value: users.value.filter((u: any) => u.status === 'banned').length, color: '#c2685c' },
])
const emptyText = computed(() => {
  if (search.value) return '没有匹配的用户'
  return { online: '当前无在线用户', unfetched: '所有用户都拉取过订阅', noplan: '所有用户都有生效中的套餐', expiring: '近 7 天没有套餐到期', banned: '没有被封禁的用户' }[filter.value] || '暂无用户'
})

const filtered = computed(() => {
  let list = users.value
  switch (filter.value) {
    case 'online': list = list.filter((u: any) => u.online); break
    case 'unfetched': list = list.filter((u: any) => !u.sub_last_fetched_at); break
    case 'noplan': list = list.filter(needsPlan); break
    case 'expiring': list = list.filter(expiringSoon); break
    case 'banned': list = list.filter((u: any) => u.status === 'banned'); break
  }
  if (search.value) {
    const q = search.value.toLowerCase()
    list = list.filter((u: any) => u.username?.toLowerCase().includes(q) || u.email?.toLowerCase().includes(q)
      || u.remark?.toLowerCase().includes(q))
  }
  if (sortBy.value === 'default') return list
  const sorted = [...list]
  sorted.sort((a: any, b: any) => {
    switch (sortBy.value) {
      case 'online': return (b.last_online_at || 0) - (a.last_online_at || 0)
      case 'subfetch': return (b.sub_last_fetched_at || 0) - (a.sub_last_fetched_at || 0)
      case 'usage': return usedPctOf(b) - usedPctOf(a) || (b.traffic?.used || 0) - (a.traffic?.used || 0)
      // 不过期的份排在最后：它们永远轮不到「需要续费」
      case 'expiry': return (a.plan_summary?.next_expiry_at || Infinity) - (b.plan_summary?.next_expiry_at || Infinity)
      case 'points': return (b.points || 0) - (a.points || 0)
    }
    return 0
  })
  return sorted
})

// ---- 头像 ----
function initial(name: string) { return (name || '?').charAt(0).toUpperCase() }
function hashHue(name: string) {
  let h = 0
  for (let i = 0; i < (name || '').length; i++) h = (h * 31 + name.charCodeAt(i)) % 360
  return h
}
function avatarStyle(name: string) {
  const h = hashHue(name)
  return { background: `hsl(${h},38%,93%)`, color: `hsl(${h},42%,32%)` }
}

// ---- 更多操作 ----
const moreOptions = [
  { label: '重置节点凭据', key: 'creds' },
  { type: 'divider', key: 'd1' },
  { label: () => h('span', { style: 'color:var(--danger)' }, '删除用户'), key: 'delete' },
]
function onMore(key: string, u: any) {
  if (key === 'creds') handleResetCreds(u)
  else if (key === 'delete') handleDelete(u)
}

// ---- 套餐面板 ----
const showPlans = ref(false)
const plansUser = ref<any>(null)
const userPlans = ref<any[]>([])
const loadingPlans = ref(false)
const planFilter = ref('all')
const aggregate = ref(true)
const openGroups = ref<Set<string>>(new Set())
const removingId = ref<number | null>(null)

function openPlans(u: any) {
  plansUser.value = u
  userPlans.value = []
  planFilter.value = 'all'
  openGroups.value = new Set()
  assignPkgId.value = null
  showPlans.value = true
  loadPlans(u.id)
  loadPackages()
}
async function loadPlans(uid: number) {
  loadingPlans.value = true
  try { userPlans.value = await apiList(`/api/admin/users/${uid}/plans`) }
  catch (e: any) { message.error('读取套餐失败：' + (e?.message || '请稍后重试')) }
  finally { loadingPlans.value = false }
}

// 一份套餐落在哪一档。用 planStatusMeta 而不是直接读 p.status，是为了和行上的
// 状态标签用同一套判定——标签写「已过期」而计数把它算成生效中，是更糟的错。
function bucketOf(p: any): 'active' | 'queued' | 'finished' {
  const label = planStatusMeta(p).label
  return label === '使用中' ? 'active' : label === '排队中' ? 'queued' : 'finished'
}
const visiblePlans = computed(() => {
  const list = planFilter.value === 'all'
    ? [...userPlans.value]
    : userPlans.value.filter((p: any) => bucketOf(p) === planFilter.value)
  list.sort((a: any, b: any) => planSortKey(a) - planSortKey(b) || (a.id - b.id))
  return list
})
// 份数只数套餐份。流量包是跨分组的余额，它自己一行、自己一个口径——把它算进
// 「生效 N 份」会和卡片上的数字对不上（卡片的 plan_summary 就没算它）。
const counts = computed(() => {
  const c = { active: 0, queued: 0, finished: 0, totalUsed: 0 }
  for (const p of userPlans.value) {
    if (p.kind !== 'pool') c[bucketOf(p)]++
    c.totalUsed += p.used || 0
  }
  return c
})
const freeUsedText = computed(() => {
  const f = trafficOf(plansUser.value)?.free_used || 0
  return f > 0 ? `，另有免费分组 ${fmtBytes(f)}` : ''
})

interface PlanGroup {
  key: string; name: string; kind: string
  items: any[]
  active: number; queued: number; finished: number
  availLimit: number; availUsed: number
  totalUsed: number; nextExpiry: number
}

// 聚合：同一个套餐（package_id）的多份合成一行。流量包（pool）单独一组——它是
// 跨分组的余额，不是一份有名字和窗口的套餐。管理员额度 / 注册赠送 package_id
// 各自固定（0 / -1），天然各成一组。
const planGroups = computed<PlanGroup[]>(() => {
  const map = new Map<string, PlanGroup>()
  for (const p of visiblePlans.value) {
    const key = p.kind === 'pool' ? 'pool' : 'pkg:' + p.package_id
    let g = map.get(key)
    if (!g) {
      g = {
        key, name: p.kind === 'pool' ? (p.name || '通用流量') : (p.name || '套餐 #' + p.id), kind: p.kind,
        items: [], active: 0, queued: 0, finished: 0,
        availLimit: 0, availUsed: 0, totalUsed: 0, nextExpiry: 0,
      }
      map.set(key, g)
    }
    g.items.push(p)
    g.totalUsed += p.used || 0
    const b = bucketOf(p)
    g[b]++
    // 可用额度只累加生效中的份，理由同卡片口径
    if (b === 'active') {
      if (p.traffic_limit > 0) { g.availLimit += p.traffic_limit; g.availUsed += p.used || 0 }
      if (p.expiry_at > 0 && (g.nextExpiry === 0 || p.expiry_at < g.nextExpiry)) g.nextExpiry = p.expiry_at
    }
  }
  // 有生效份的组排前面，其次排队，最后只剩已结束的
  return [...map.values()].sort((a, b) =>
    (b.active ? 2 : b.queued ? 1 : 0) - (a.active ? 2 : a.queued ? 1 : 0) || a.key.localeCompare(b.key))
})
function groupPct(g: PlanGroup) { return g.availLimit > 0 ? pct(g.availUsed, g.availLimit) : 0 }
function groupBarWidth(g: PlanGroup) {
  if (g.availLimit > 0) return Math.min(groupPct(g), 100) + '%'
  return '0%'
}
function groupAvailText(g: PlanGroup) {
  if (g.availLimit > 0) return `${fmtBytes(g.availUsed)} / ${fmtBytes(g.availLimit)}`
  return g.queued ? '待启用' : '无可用额度'
}
function groupNote(g: PlanGroup) {
  const parts: string[] = []
  if (g.availLimit > 0) parts.push(`可用剩余 ${fmtBytes(Math.max(g.availLimit - g.availUsed, 0))}`)
  if (g.items.length > 1) parts.push(`${g.items.length} 份累计用量 ${fmtBytes(g.totalUsed)}`)
  if (g.nextExpiry) parts.push(`${g.active > 1 ? '最近 ' : ''}${fmtDate(g.nextExpiry)} 到期`)
  else if (g.active) parts.push('不过期')
  // 一个已经全部结束的组还是要说点什么：它到底用掉了多少、什么时候结束的，
  // 否则这一行只剩「无可用额度」，看不出它有没有被用过。
  if (!parts.length) {
    if (g.items.length === 1) parts.push(`累计用量 ${fmtBytes(g.totalUsed)}`)
    if (g.queued) parts.push(`${g.queued} 份待启用`)
    const last = Math.max(...g.items.map((p: any) => p.expiry_at || 0))
    if (last > 0) parts.push(`${fmtDate(last)} 结束`)
  }
  return parts.join(' · ')
}
function toggleGroup(key: string) {
  const s = new Set(openGroups.value)
  s.has(key) ? s.delete(key) : s.add(key)
  openGroups.value = s
}

// 移除一份额度。刻意不是退款：积分不退、订单记录不动，所以文案必须把这点说清楚，
// 否则管理员会拿它当「撤销购买」用，用户则莫名其妙少了一份还没拿回积分。
function removePlan(p: any) {
  const u = plansUser.value
  if (!u) return
  const isPool = p.kind === 'pool'
  const queued = p.status === 'queued'
  const quota = fmtBytes(p.traffic_limit)
  dialog.warning({
    title: isPool ? '确认清空流量包' : queued ? '确认移除未生效套餐' : '确认移除套餐',
    content: isPool
      ? `清空「${u.username}」的流量包（通用流量）余额？该余额立即失效，已用记录一并移除。`
        + '这不是退款：积分不会退回，订单记录保持不变。'
      : queued
        ? `移除「${u.username}」尚未生效的「${p.name}」？这一份还没开始计量，其全部额度（${quota}）将一并收回。`
          + '这不是退款：积分不会退回，订单记录保持不变，需要退款请到「订单」里操作。'
        : `移除「${u.username}」的「${p.name}」这一份？该份额度（含剩余流量）立即失效，订阅中对应的节点会被撤下；`
          + '若同一套餐还有排队中的份，会立刻顶上来。'
          + '这不是退款：积分不会退回，订单记录保持不变，需要退款请到「订单」里操作。',
    positiveText: isPool ? '清空' : '移除',
    negativeText: '取消',
    onPositiveClick: async () => {
      removingId.value = p.id
      try {
        await apiDelete(`/api/admin/users/${u.id}/plans/${p.id}`)
        message.success(isPool ? '流量包已清空' : '套餐已移除')
        await Promise.all([loadPlans(u.id), load()])
        syncPlansUser()
      } catch (e: any) { message.error(e.message) }
      finally { removingId.value = null }
    },
  })
}
// 按份加减流量。额度写在这一份自己的桶上，所以必须指定是哪一份，
// 不能像积分那样对着用户一个总数下手。
const showAdjust = ref(false)
const adjustPlan = ref<any>(null)
const adjustGB = ref<number | null>(null)
const GiB = 1024 * 1024 * 1024
const adjustAmountText = computed(() => {
  const p = adjustPlan.value
  if (!p) return ''
  return `${fmtBytes(p.used || 0)} / ${fmtBytes(p.traffic_limit)}`
})
function openAdjust(p: any) {
  adjustPlan.value = p
  adjustGB.value = null
  showAdjust.value = true
}
async function handleAdjust() {
  const u = plansUser.value
  const p = adjustPlan.value
  if (!u || !p || !adjustGB.value) return
  saving.value = true
  try {
    await apiPost(`/api/admin/users/${u.id}/plans/${p.id}/traffic`, {
      delta_bytes: Math.round(adjustGB.value * GiB),
    })
    message.success(adjustGB.value > 0 ? '已增加流量' : '已扣减流量')
    showAdjust.value = false
    await Promise.all([loadPlans(u.id), load()])
    syncPlansUser()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// 列表重载后，面板里的用户对象要指向新的那份，否则顶部汇总还是旧数字
function syncPlansUser() {
  if (!plansUser.value) return
  const fresh = users.value.find((x: any) => x.id === plansUser.value.id)
  if (fresh) plansUser.value = fresh
}

// ---- 分配套餐（在套餐面板内）----
const assignPkgId = ref<number | null>(null)
const assignDays = ref<number | null>(null)
const pkgOptions = ref<any[]>([])
const pkgList = ref<any[]>([])
const assignPkg = computed(() => pkgList.value.find((p: any) => p.id === assignPkgId.value) || null)
function packageQueueKey(pkg: any): string { return pkg?.queue_key || (pkg?.id ? `pkg:${pkg.id}` : '') }
const assignWillQueue = computed(() =>
  !!assignPkg.value && assignPkg.value.type === 'plan' && userPlans.value.some((p: any) =>
    p.kind === 'plan' && bucketOf(p) === 'active' &&
    (p.queue_key || `pkg:${p.package_id}`) === packageQueueKey(assignPkg.value)))
// 天数输入只对订阅计划出现。流量包加的是共享池，填天数既不会到期也不会改额度。
const assignIsPlan = computed(() => assignPkg.value?.type === 'plan')
function defaultAssignDays(pkg: any): number | null {
  if (!pkg || pkg.type !== 'plan') return null
  const opts = pkg.options || []
  if (opts.length) return opts[0].days
  return pkg.duration_days > 0 ? pkg.duration_days : null
}
const assignDayChips = computed(() => {
  const pkg = assignPkg.value
  if (!pkg || pkg.type !== 'plan') return []
  const opts = pkg.options || []
  if (opts.length) return opts.map((o: any) => o.days)
  return pkg.duration_days > 0 ? [pkg.duration_days] : []
})
// 自定义天数封顶 3650，但已上架档位可以更长——输入框不能把快捷档夹成一个「自定义天数」。
const assignDaysMax = computed(() => Math.max(3650, ...assignDayChips.value, 0))
watch(assignPkgId, () => { assignDays.value = defaultAssignDays(assignPkg.value) })
async function loadPackages() {
  if (pkgOptions.value.length) return
  try {
    const pkgs = await apiList<any>('/api/admin/packages')
    pkgList.value = pkgs
    pkgOptions.value = pkgs.map((p: any) => ({ label: `${p.name} (${p.type})`, value: p.id }))
  } catch {}
}
async function handleAssign() {
  if (!assignPkgId.value || !plansUser.value) { message.warning('请选择套餐'); return }
  saving.value = true
  try {
    await apiPost(`/api/admin/users/${plansUser.value.id}/assign-plan`,
      { package_id: assignPkgId.value, duration_days: assignIsPlan.value ? Math.round(assignDays.value || 0) : 0 })
    message.success(assignWillQueue.value ? '已分配并加入队列' : '分配成功')
    assignPkgId.value = null
    await Promise.all([loadPlans(plansUser.value.id), load()])
    syncPlansUser()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// --- Create ---
const showCreate = ref(false)
const newUser = reactive({ username: '', email: '', password: '', points: 0, group_ids: [] as number[], remark: '' })
function openCreate() { Object.assign(newUser, { username: '', email: '', password: '', points: 0, group_ids: [], remark: '' }); showCreate.value = true }
async function handleCreate() {
  saving.value = true
  try { await apiPost('/api/admin/users', newUser); message.success('创建成功'); showCreate.value = false; await load() } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// --- Edit ---
const showEdit = ref(false)
const editUser = ref<any>(null)
const manualEnabled = ref(false)
const editTrafficGB = ref(0)
const editExpiry = ref('')
const editBanned = ref(false)
const resetPw = ref('')
const resetTraffic = ref(false)
const editGroupIDs = ref<number[]>([])
const editRemark = ref('')
const editEmail = ref('')
async function openEdit(u: any) {
  editUser.value = { ...u }
  editBanned.value = u.status === 'banned'
  editRemark.value = u.remark || ''
  editEmail.value = u.email || ''
  resetPw.value = ''; resetTraffic.value = false
  editGroupIDs.value = [...(u.group_ids || [])]
  // Prefill the manual-grant fields from the user's admin-grant bucket itself (not
  // the aggregate traffic_limit), so saving sets exactly that bucket — no double
  // counting against their purchased plans, and no accidental grant when there's none.
  manualEnabled.value = false
  editTrafficGB.value = 0
  editExpiry.value = ''
  showEdit.value = true
  try {
    const plans = await apiList(`/api/admin/users/${u.id}/plans`)
    const grant = plans.find((p: any) => p.kind === 'plan' && p.package_id === 0)
    if (grant) {
      manualEnabled.value = true
      editTrafficGB.value = (grant.traffic_limit || 0) / (1024 * 1024 * 1024)
      editExpiry.value = toLocalDatetimeInput(grant.expiry_at)
    }
  } catch { /* leave the "no grant" defaults on error */ }
}
async function handleSave() {
  if (!editUser.value) return
  saving.value = true
  try {
    const body: any = {
      status: editBanned.value ? 'banned' : 'active',
      manual_enabled: manualEnabled.value,
      manual_traffic: manualEnabled.value ? Math.round(editTrafficGB.value * 1024 * 1024 * 1024) : 0,
      manual_expiry: manualEnabled.value && editExpiry.value ? Math.floor(new Date(editExpiry.value).getTime() / 1000) : 0,
    }
    if (resetPw.value) body.password = resetPw.value
    if (resetTraffic.value) body.reset_traffic = true
    body.group_ids = editGroupIDs.value
    // 总是发：'' 是「清除备注」这个明确意图，不是「没填」。
    body.remark = editRemark.value
    // 邮箱只在真的改了才发。后端每次真写入都会作废用户手上未用的验证令牌，
    // 没必要为一次「只改了封禁开关」的保存把它顺手废掉。
    const curEmail = editUser.value.email || ''
    if (editEmail.value.trim() !== curEmail) body.email = editEmail.value.trim()
    await apiPut(`/api/admin/users/${editUser.value.id}`, body)
    message.success('保存成功'); showEdit.value = false; await load(); syncPlansUser()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// --- Recharge ---
const showRecharge = ref(false)
const rechargeUser = ref<any>(null)
const rechargeAmount = ref(0)
const rechargeNote = ref('')
function openRecharge(u: any) { rechargeUser.value = u; rechargeAmount.value = 0; rechargeNote.value = ''; showRecharge.value = true }
async function handleRecharge() {
  saving.value = true
  try {
    await apiPost(`/api/admin/users/${rechargeUser.value.id}/points`, { amount: rechargeAmount.value, note: rechargeNote.value })
    message.success(rechargeAmount.value >= 0 ? '充值成功' : '扣除成功'); showRecharge.value = false; await load(); syncPlansUser()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// --- Orders ---
const showOrders = ref(false)
const ordersUser = ref<any>(null)
const userOrders = ref<any[]>([])
const loadingOrders = ref(false)
const refundShow = ref(false)
const refundId = ref<number | null>(null)
async function openOrders(u: any) {
  ordersUser.value = u; showOrders.value = true; loadingOrders.value = true
  try { userOrders.value = await apiList(`/api/admin/users/${u.id}/orders`) } catch {} finally { loadingOrders.value = false }
}
function openRefund(orderId: number) {
  refundId.value = orderId
  refundShow.value = true
}
async function reloadUserOrders() {
  if (!ordersUser.value) return
  // Refunding also changes the user's points/quota, so refresh both the order
  // list and the user table behind the modal.
  try { userOrders.value = await apiList(`/api/admin/users/${ordersUser.value.id}/orders`) } catch {}
  await load()
  syncPlansUser()
  // 退款会撤掉对应的那一份，套餐面板要是开着就得跟着刷新
  if (showPlans.value && plansUser.value) await loadPlans(plansUser.value.id)
}

// --- Reset node credentials ---
// The operator half of「订阅泄露怎么办」. Swapping the user's subscription address
// only moves where the list is served; the node links already exported from the
// old address authenticate with the account's own credentials and keep working
// until those are rotated — which is what this does.
const resettingCreds = ref<number | null>(null)
function handleResetCreds(u: any) {
  dialog.error({
    title: '确认重置节点凭据',
    content: `为用户「${u.username}」重新生成所有节点凭据？从其旧订阅导出的节点将立即失效，`
      + '该用户需要重新导入订阅。凭据会马上推送到相关服务器，'
      + '推送会重启这些服务器上的 sing-box，其他用户的在线连接也会短暂中断。',
    positiveText: '重置', negativeText: '取消',
    onPositiveClick: async () => {
      resettingCreds.value = u.id
      try {
        await apiPost(`/api/admin/users/${u.id}/reset-node-creds`)
        message.success('已重置并推送，该用户需重新导入订阅')
      } catch (e: any) { message.error(e.message) }
      finally { resettingCreds.value = null }
    },
  })
}

// --- Delete ---
function handleDelete(u: any) {
  dialog.warning({
    title: '确认删除用户',
    content: `确定删除用户「${u.username}」？其订阅、套餐与设备将一并失效，此操作不可撤销。`,
    positiveText: '删除', negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await apiDelete(`/api/admin/users/${u.id}`)
        message.success('已删除')
        if (plansUser.value?.id === u.id) showPlans.value = false
        await load()
      } catch (e: any) { message.error(e.message) }
    },
  })
}

// --- User groups ---
const userGroups = ref<any[]>([])
const userGroupOptions = computed(() => userGroups.value.map(g => ({ label: g.name, value: g.id })))
function groupNames(ids?: number[]) {
  return (ids || []).map(id => userGroups.value.find(g => g.id === id)?.name).filter(Boolean).join('、')
}

async function load() {
  loading.value = true
  try {
    const [us, gs] = await Promise.all([
      apiList('/api/admin/users'),
      apiList('/api/admin/user-groups').catch(() => []),
    ])
    users.value = us; userGroups.value = gs
  } catch (e: any) { message.error('加载失败：' + (e?.message || '请稍后重试')) } finally { loading.value = false }
}
onMounted(load)
</script>

<style scoped>
.page-sub { color: var(--text-2); margin: 0; font-size: 13px; }
.au-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; }
.au-actions { display: flex; gap: 8px; flex-shrink: 0; }
.au-count { font-size: 12px; color: var(--text-3); font-variant-numeric: tabular-nums; }
.modal-sub { font-size: 13px; color: var(--text-2); margin-bottom: 12px; }
.form-hint { margin-top: 4px; font-size: 12px; color: var(--text-3); line-height: 1.5; }

/* 概览条：既是数字也是筛选器 */
.stat-strip { display: grid; grid-template-columns: repeat(auto-fit, minmax(104px, 1fr)); gap: 8px; margin-bottom: 14px; }
.ss-item {
  display: flex; flex-direction: column; gap: 2px; padding: 10px 12px; text-align: left;
  background: var(--card); border: 1px solid var(--border); border-radius: var(--r-sm);
  font: inherit; color: inherit; cursor: pointer;
  transition: border-color .16s, box-shadow .16s, transform .16s;
}
.ss-item:hover { border-color: #d5d5d5; box-shadow: var(--shadow-sm); transform: translateY(-1px); }
.ss-item.on { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent) inset; }
.ss-item:focus-visible { outline: 2px solid var(--accent-strong); outline-offset: 2px; }
.ss-val { font-size: 20px; font-weight: 720; line-height: 1.1; font-variant-numeric: tabular-nums; letter-spacing: -0.02em; }
.ss-label { font-size: 11.5px; color: var(--text-3); }

/* 用户卡片 */
.user-card {
  display: flex; flex-direction: column; gap: 10px; padding: 14px;
  background: var(--card); border: 1px solid var(--border); border-radius: var(--r-sm);
  transition: box-shadow .18s, border-color .18s;
}
.user-card:hover { box-shadow: var(--shadow); border-color: #d5d5d5; }
.uc-head { display: flex; align-items: center; gap: 10px; min-width: 0; }
.uc-avatar {
  width: 36px; height: 36px; flex-shrink: 0; border-radius: 10px;
  display: flex; align-items: center; justify-content: center;
  font-weight: 700; font-size: 15px;
}
.uc-id { min-width: 0; flex: 1; }
.uc-name-row { display: flex; align-items: center; gap: 6px; min-width: 0; }
.uc-name { font-weight: 650; font-size: 14.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.uc-sub { font-size: 11.5px; color: var(--text-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 备注和邮箱行区分开：略深一点、单行截断，长备注靠 title 看全 */
.uc-remark {
  margin-top: 2px; font-size: 11.5px; color: var(--text-2);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.dot-live { width: 7px; height: 7px; border-radius: 50%; background: #6f8f76; flex-shrink: 0; animation: pulse 2s ease-in-out infinite; }

.uc-block {
  display: block; width: 100%; text-align: left; font: inherit; color: inherit;
  background: var(--bg-soft); border: 1px solid transparent; border-radius: 8px; padding: 8px 10px;
}
.uc-plans { cursor: pointer; transition: background .16s, border-color .16s; }
.uc-plans:hover { background: var(--accent-soft); border-color: var(--border); }
.uc-plans:hover .uc-arrow { opacity: 1; transform: translateX(2px); }
.uc-plans:focus-visible { outline: 2px solid var(--accent-strong); outline-offset: 1px; }
.uc-row { display: flex; align-items: center; gap: 8px; min-width: 0; }
.uc-k { font-size: 11.5px; color: var(--text-3); flex-shrink: 0; }
.uc-v { font-size: 13px; font-weight: 600; font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.uc-pct { margin-left: auto; font-size: 12px; font-weight: 700; font-variant-numeric: tabular-nums; }
.uc-arrow { margin-left: auto; color: var(--text-3); opacity: .5; transition: opacity .18s, transform .18s; }
.uc-note { font-size: 11px; color: var(--text-3); margin-top: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.uc-chips { display: flex; gap: 4px; flex-wrap: wrap; min-width: 0; }
.uc-meta { display: flex; flex-wrap: wrap; gap: 4px 14px; font-size: 12.5px; color: var(--text-2); }
.uc-meta .kv b { font-weight: 600; color: var(--text); }
.uc-foot { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.uc-foot .spacer { flex: 1; }

.bar { height: 5px; border-radius: 3px; background: var(--border); overflow: hidden; margin-top: 6px; }
.bar.slim { height: 4px; margin-top: 4px; }
.bar-fill { height: 100%; border-radius: 3px; transition: width .6s cubic-bezier(.22, 1, .36, 1), background .3s ease; }

/* 状态小片 */
.chip { font-style: normal; font-size: 10.5px; font-weight: 650; padding: 1px 7px; border-radius: 20px; white-space: nowrap; }
.chip.ok { background: #6f8f761f; color: #4e6b55; }
.chip.q { background: #5e7a991f; color: #4c6480; }
.chip.fin { background: #7676761a; color: var(--text-3); }
.chip.none { background: transparent; color: var(--text-3); font-weight: 500; padding-left: 0; }

/* 套餐面板 */
.pm-summary { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; margin-bottom: 14px; }
.pm-stat { background: var(--bg-soft); border-radius: var(--r-sm); padding: 10px 12px; min-width: 0; }
.pm-label { display: block; font-size: 11.5px; color: var(--text-3); }
.pm-val { display: block; font-size: 17px; font-weight: 700; margin-top: 2px; font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pm-val em { font-style: normal; font-size: 12px; font-weight: 550; color: var(--text-3); }
/* 汇总提示可能包含剩余额度与免费分组用量，窄屏下允许折行。 */
.pm-hint { display: block; font-size: 11px; color: var(--text-3); font-style: normal; margin-top: 2px; line-height: 1.45; }
.pm-bar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 10px; }
.pm-bar .spacer { flex: 1; }

.grp { border: 1px solid var(--border); border-radius: var(--r-sm); padding: 10px 12px; margin-bottom: 8px; }
.grp-head { display: flex; align-items: center; gap: 8px; width: 100%; background: none; border: none; padding: 0; font: inherit; color: inherit; cursor: pointer; text-align: left; min-width: 0; }
.grp-head:focus-visible { outline: 2px solid var(--accent-strong); outline-offset: 2px; }
.chev { display: inline-block; color: var(--text-3); transition: transform .2s ease; flex-shrink: 0; }
.chev.open { transform: rotate(90deg); }
.grp-name { font-weight: 650; font-size: 13.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.grp-chips { display: flex; gap: 4px; flex-wrap: wrap; }
.grp-num { margin-left: auto; font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
.grp-note { font-size: 11px; color: var(--text-3); margin-top: 5px; }
.grp-items { margin-top: 10px; padding-top: 8px; border-top: 1px dashed var(--border); display: flex; flex-direction: column; gap: 8px; }
.flat { display: flex; flex-direction: column; gap: 8px; }

.pm-assign { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--border); }
.pm-assign-title { font-size: 12.5px; font-weight: 650; color: var(--text-2); margin-bottom: 8px; }
.pm-assign-row { display: flex; gap: 8px; align-items: center; }
.pm-assign-chips { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
.pm-assign-chips-label { font-size: 11.5px; color: var(--text-3); }
.pm-assign-tip { margin-top: 6px; font-size: 11.5px; color: var(--text-3); line-height: 1.5; }
.pm-assign-hint { margin-top: 8px; font-size: 11.5px; color: var(--info); line-height: 1.5; }

@media (prefers-reduced-motion: reduce) {
  .bar-fill, .chev, .ss-item, .uc-arrow { transition: none; }
  .dot-live { animation: none; }
}
</style>

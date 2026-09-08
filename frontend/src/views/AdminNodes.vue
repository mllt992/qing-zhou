<template>
  <div>
    <div class="page-head">
      <div><h2 class="page-title">节点管理</h2><p class="page-sub">节点、分组、订阅源与完整出网链路集中管理</p></div>
    </div>
    <div class="resource-overview">
      <button class="resource-metric" type="button" @click="tab='nodes'"><b>{{ nodes.length }}</b><span>全部节点 · 启用 {{ nodes.filter(n => n.enabled).length }}</span></button>
      <button class="resource-metric" type="button" @click="tab='nodes'"><b>{{ groups.length }}</b><span>节点分组 · AI 路由 {{ groups.filter(g => g.is_ai).length }}</span></button>
      <button class="resource-metric" type="button" @click="tab='sources'"><b>{{ sources.length }}</b><span>订阅源 · 启用 {{ sources.filter(s => s.enabled).length }}</span></button>
      <button class="resource-metric" type="button" @click="tab='topology'"><b>{{ inbounds.length }}</b><span>关联入站 · 出口 {{ egresses.length }}</span></button>
    </div>
    <n-tabs v-model:value="tab" animated>
      <!-- 节点（按分组卡片展示，同组节点聚在一张卡片里） -->
      <n-tab-pane name="nodes" tab="节点">
        <div class="page-toolbar">
          <span class="spacer" />
          <n-button size="small" @click="openGroup()">添加分组</n-button>
          <n-button size="small" @click="openNodeImport">批量导入</n-button>
          <n-button size="small" type="primary" @click="openNode()">添加节点</n-button>
        </div>
        <n-spin :show="loading">
          <div v-if="groupedView.length">
            <n-card v-for="gv in groupedView" :key="gv.key" size="small" :title="gv.name" class="group-card">
              <template #header-extra>
                <div class="group-actions">
                  <n-tag v-if="gv.group?.is_ai" size="tiny" type="success" :bordered="false">AI 路由</n-tag>
                  <n-tag size="tiny" :type="gv.isUngrouped ? 'default' : 'info'" :bordered="false">{{ gv.nodes.length }} 节点</n-tag>
                  <n-button size="tiny" type="primary" secondary @click="openNodeInGroup(gv.group)">＋ 添加节点</n-button>
                  <n-button size="tiny" :disabled="!reuseCatalog.length" @click="openReuse(gv.group)">复用入口</n-button>
                  <n-button v-if="gv.group" size="tiny" quaternary @click="openGroup(gv.group)">编辑</n-button>
                  <n-button v-if="gv.group" size="tiny" type="error" quaternary @click="handleDeleteGroup(gv.group.id)">删除</n-button>
                </div>
              </template>
              <div v-if="gv.description" class="group-desc">{{ gv.description }}</div>
              <div v-if="gv.nodes.length" class="card-grid">
                <div v-for="(r, idx) in gv.nodes" :key="r.id" class="list-card">
                  <div class="lc-head">
                    <span class="lc-title">{{ r.name || '—' }}</span>
                    <n-tag :type="r.enabled ? 'success' : 'default'" size="tiny" :bordered="false">{{ r.enabled ? '启用' : '禁用' }}</n-tag>
                  </div>
                  <div class="lc-meta">
                    <span class="kv"><n-tag :type="r.type === 'self_built' ? 'info' : 'warning'" size="tiny" :bordered="false">{{ r.type === 'self_built' ? '自建' : '外部' }}</n-tag></span>
                    <span class="kv">协议 <b>{{ nodeProtocol(r) }}</b></span>
                    <!-- 节点跑在哪台机器上。外部节点不在我们的机器上，自建但入站失踪时
                         也没有答案——那两种情况下面的链路行会说明，这里就不占位了。 -->
                    <span v-if="nodeServer(r)" class="kv">机器 <b>{{ nodeServer(r) }}</b></span>
                    <span v-if="inboundReuse(r) > 1" class="kv"><n-tag size="tiny" type="success" :bordered="false">入口复用 ×{{ inboundReuse(r) }}</n-tag></span>
                  </div>
                  <div v-if="(r.group_ids || []).length > 1" class="lc-meta"><span class="kv">分组 <b>{{ groupNames(r.group_ids) }}</b></span></div>
                  <div class="route-preview" :class="{ warn: cardRoute(r).broken }">
                    <div class="route-track">
                      <template v-for="(step, si) in cardRoute(r).steps" :key="`${step.kind}-${si}`">
                        <span v-if="si" class="route-edge"><i></i></span>
                        <span class="route-step" :class="step.kind">
                          <i class="route-dot"></i>
                          <span class="route-copy">
                            <small>{{ step.role }}</small>
                            <b :title="step.label">{{ step.label }}</b>
                          </span>
                        </span>
                      </template>
                    </div>
                    <div v-if="cardRoute(r).warning" class="route-warning">{{ cardRoute(r).warning }}</div>
                  </div>
                  <div class="lc-foot">
                    <span class="order-actions">
                      <n-button size="tiny" quaternary circle :disabled="idx === 0 || reordering" title="前移（订阅/列表更靠前）" @click="moveNodeInGroup(gv, idx, -1)">←</n-button>
                      <n-button size="tiny" quaternary circle :disabled="idx === gv.nodes.length - 1 || reordering" title="后移" @click="moveNodeInGroup(gv, idx, 1)">→</n-button>
                    </span>
                    <span class="node-actions">
                      <n-button size="tiny" quaternary @click="openNode(r)">编辑</n-button>
                      <n-button size="tiny" type="error" quaternary @click="handleDeleteNode(r.id)">删除</n-button>
                    </span>
                  </div>
                </div>
              </div>
              <n-empty v-else description="该分组暂无节点" size="small" style="padding:12px 0;">
                <template #extra><n-button size="tiny" @click="openNodeInGroup(gv.group)">添加节点</n-button></template>
              </n-empty>
            </n-card>
          </div>
          <n-empty v-else-if="!loading" description="暂无分组或节点" style="padding:40px 0;" />
        </n-spin>
      </n-tab-pane>

      <!-- 链路拓扑：按节点分组展示每个节点的完整出网路径 -->
      <n-tab-pane name="topology" tab="链路拓扑">
        <n-spin :show="loading">
          <div v-if="nodes.length" class="topo">
            <div class="topo-legend">
              <span><i class="dot client"></i>客户端</span>
              <span><i class="dot entry"></i>节点入口</span>
              <span><i class="dot landing"></i>落地入站</span>
              <span><i class="dot egress"></i>代理出口</span>
              <span><i class="dot inet"></i>互联网</span>
              <span style="flex:1;"></span>
              <n-button size="tiny" quaternary @click="showTopoIp = !showTopoIp">{{ showTopoIp ? '🙈 隐藏 IP' : '👁 显示 IP' }}</n-button>
            </div>
            <div v-for="gv in topoGroups" :key="gv.key" class="topo-machine">
              <div class="topo-mhead">
                <span class="machine-name">{{ gv.name }}</span>
                <n-tag size="tiny" :type="gv.isUngrouped ? 'default' : 'info'" :bordered="false">{{ gv.rows.length }} 节点</n-tag>
              </div>
              <n-empty v-if="!gv.rows.length" description="该分组暂无节点" size="small" style="padding:10px 0;" />
              <div v-for="row in gv.rows" :key="row.node.id" class="topo-row" :class="{ off: row.off }">
                <span class="topo-node client"><small>起点</small><b>客户端</b></span>
                <span class="topo-arrow"><i></i></span>
                <span class="topo-node entry">
                  <small>入口</small>
                  <span class="topo-main"><b>{{ row.node.name || '—' }}</b><span class="topo-proto">{{ nodeProtocol(row.node) }}</span></span>
                  <template v-if="row.ib">
                    <span class="topo-detail"><span class="topo-port">:{{ row.ib.listen_port }}</span><span class="topo-loc">@ {{ serverName(row.ib.server_id) }}</span></span>
                  </template>
                </span>
                <span v-if="row.kind === 'external'" class="topo-arrow relay"><i></i><em>外部线路</em></span>
                <span v-else-if="row.kind === 'broken'" class="topo-arrow relay warn"><i></i><em>入站失效</em></span>
                <template v-else>
                <template v-for="(seg, si) in row.segs" :key="si">
                  <template v-if="seg.kind === 'landing'">
                    <span class="topo-arrow relay"><i></i><em>{{ seg.logical ? '固定落地' : '中转' }}</em></span>
                    <span class="topo-node landing">
                      <small>{{ seg.logical ? '固定落地' : '落地' }}</small>
                      <span class="topo-main"><b>{{ seg.ib.tag }}</b><span class="topo-proto">{{ (seg.ib.type || '').toUpperCase() }}</span></span>
                      <span class="topo-detail"><span class="topo-loc">@ {{ serverName(seg.ib.server_id) }}</span><span v-if="seg.off" class="topo-loc">· 已停用</span></span>
                    </span>
                  </template>
                  <template v-else-if="seg.kind === 'egress'">
                    <span class="topo-arrow egress"><i></i><em>代理出口</em></span>
                    <span class="topo-node egress">
                      <small>代理出口</small>
                      <span class="topo-main"><b>{{ seg.eg.name }}</b><span class="topo-proto">{{ (seg.eg.type || '').toUpperCase() }}</span></span>
                      <span class="topo-detail"><span class="topo-loc">{{ showTopoIp ? seg.eg.host : maskHost(seg.eg.host) }}</span></span>
                    </span>
                  </template>
                  <span v-else-if="seg.kind === 'broken-landing'" class="topo-arrow relay warn"><i></i><em>落地失效</em></span>
                  <span v-else-if="seg.kind === 'broken-egress'" class="topo-arrow relay warn"><i></i><em>出口失效</em></span>
                  <span v-else-if="seg.kind === 'loop'" class="topo-arrow relay warn"><i></i><em>链路成环</em></span>
                  <span v-else-if="seg.kind === 'route-broken'" class="topo-arrow relay warn"><i></i><em>固定落地已删除</em></span>
                  <span v-else-if="seg.kind === 'route-disabled'" class="topo-arrow relay warn"><i></i><em>落地链路已停用</em></span>
                  <!-- 原本该走中转，落地被删后降级成本机直连；出口 IP 已经变了。 -->
                  <span v-else-if="seg.kind === 'downgraded'" class="topo-arrow relay warn"
                        title="原落地入站已被删除，此中转已降级为从本机直连出网；编辑并保存该入站可消除此提示">
                    <i></i><em>原落地已删除 · 现本机直连</em>
                  </span>
                </template>
                </template>
                <template v-if="routeStopped(row)">
                  <span class="topo-node inet stopped"><small>状态</small><b>线路已停用</b></span>
                </template>
                <template v-else>
                  <span class="topo-arrow"><i></i></span>
                  <span class="topo-node inet"><small>终点</small><b>互联网</b></span>
                </template>
                <span class="topo-actions">
                  <!-- 降级提示要么靠重新指定落地/出口来消除，要么在这里明确「就这样」。
                       不给出口就成了一条永远消不掉的红字，久了没人再看。 -->
                  <n-button v-if="row.ib?.upstream_broken" size="tiny" quaternary
                            :loading="acking === row.ib.id" @click="ackUpstream(row.ib)">已知晓</n-button>
                  <n-button size="tiny" quaternary @click="openNode(row.node)">配置</n-button>
                </span>
              </div>
            </div>
            <p class="topo-tip">
              在节点编辑中选择「固定落地」，即可让多条逻辑节点复用同一个入口 IP、端口、TLS 和协议；未指定时继续沿用 sing-box 入站原有链路。外部节点的后续链路由对方线路决定，面板不可见。
            </p>
          </div>
          <n-empty v-else-if="!loading" description="暂无节点，无法展示链路" style="padding:40px 0;" />
        </n-spin>
      </n-tab-pane>

      <!-- 订阅源 -->
      <n-tab-pane name="sources" tab="订阅源">
        <div class="page-toolbar">
          <span class="spacer" />
          <n-button size="small" type="primary" @click="openSource()">添加订阅源</n-button>
        </div>
        <n-spin :show="loading">
          <div v-if="sources.length" class="card-grid">
            <div v-for="r in sources" :key="r.id" class="list-card">
              <div class="lc-head">
                <span class="lc-title">{{ r.name || '—' }}</span>
                <span style="display:flex;gap:4px;flex-shrink:0;">
                  <!-- 停用的源后台不会去拉，节点数会一直停在上次的值。不标出来的话，
                       它和「拉到 0 个」长得一模一样。 -->
                  <n-tag v-if="!r.enabled" size="tiny" type="warning" :bordered="false">已停用</n-tag>
                  <n-tag size="tiny" :type="r.last_error ? 'error' : 'default'" :bordered="false">{{ r.last_count || 0 }} 节点</n-tag>
                </span>
              </div>
              <div class="lc-meta" style="word-break:break-all;"><span class="kv">{{ r.url }}</span></div>
              <div class="lc-meta">
                <span class="kv">上次拉取 <b>{{ r.last_fetched ? timeAgo(r.last_fetched) : '从未' }}</b></span>
                <span v-if="r.last_fetched" class="kv" style="color:var(--text-3);">{{ fmtDateTime(r.last_fetched) }}</span>
              </div>
              <!-- 后台每隔一段自动拉一次，失败时节点会被清成 0，订阅里也就没了。
                   错误只进了 last_error 和服务端日志，面板上必须说出来。 -->
              <div v-if="r.last_error" class="src-err" :title="r.last_error">拉取失败：{{ r.last_error }}</div>
              <div class="lc-foot">
                <n-button size="tiny" type="primary" @click="handleFetchSource(r.id)">拉取</n-button>
                <n-button size="tiny" @click="openSource(r)">编辑</n-button>
                <n-button size="tiny" type="error" @click="handleDeleteSource(r.id)">删除</n-button>
              </div>
            </div>
          </div>
          <n-empty v-else-if="!loading" description="暂无订阅源" style="padding:40px 0;" />
        </n-spin>
      </n-tab-pane>
    </n-tabs>

    <n-modal v-model:show="showReuse" preset="card" :title="`复用物理入口 · ${reuseTargetName}`"
             class="reuse-modal" style="width:min(640px, calc(100vw - 32px));">
      <div class="reuse-intro">
        <span>目标分组</span><b>{{ reuseTargetName }}</b>
        <p>从全局入口中选择一个；新线路只复用入口地址、端口、TLS 和协议，固定落地在下一步单独选择。</p>
      </div>
      <n-input v-model:value="reuseSearch" clearable placeholder="搜索地区、机器、协议、端口、Tag 或来源分组" />
      <div v-if="filteredReuseCatalog.length" class="reuse-list">
        <button v-for="entry in filteredReuseCatalog" :key="entry.tag" type="button" class="reuse-entry" @click="chooseReuseEntry(entry)">
          <span class="reuse-entry-main">
            <span class="reuse-entry-head">
              <b>{{ entry.serverLabel }}</b>
              <span class="reuse-proto">{{ entry.protocol }} :{{ entry.port }}</span>
              <span v-if="isInReuseTarget(entry)" class="reuse-current">当前分组</span>
            </span>
            <span class="reuse-machine">{{ entry.serverName }}</span>
            <span class="reuse-tag">{{ entry.tag }}</span>
            <span class="reuse-groups">来源：{{ entry.sourceGroups.join('、') || '未分组' }} · 已复用 {{ entry.count }} 条线路</span>
          </span>
          <span class="reuse-pick">选择 <i>→</i></span>
        </button>
      </div>
      <n-empty v-else description="没有匹配的可复用入口" size="small" style="padding:28px 0 12px;" />
    </n-modal>

    <!-- 节点编辑抽屉 -->
    <n-drawer v-model:show="showNode" :width="drawerW" placement="right">
      <n-drawer-content :title="editingNode ? '编辑节点' : '添加节点'" closable>
        <n-form label-placement="left" label-width="80">
          <n-form-item label="名称"><n-input v-model:value="nodeForm.name" /></n-form-item>
          <n-form-item label="订阅备注">
            <div style="width:100%;">
              <n-input v-model:value="nodeForm.remark" placeholder="可选；优先作为订阅展示名，不影响计量 identity" />
              <div class="form-tip">留空则用上方名称。客户端手动选中靠展示名记忆，请保持稳定。</div>
            </div>
          </n-form-item>
          <n-form-item label="类型">
            <n-radio-group v-model:value="nodeForm.type">
              <n-radio value="self_built">自建</n-radio>
              <n-radio value="external">外部</n-radio>
            </n-radio-group>
          </n-form-item>
          <n-form-item v-if="nodeForm.type === 'self_built'" label="入站 Tag">
            <n-select v-model:value="nodeForm.inbound_tag" :options="inboundOptions" placeholder="选择入站" />
          </n-form-item>
          <n-form-item v-if="nodeForm.type === 'self_built'" label="固定落地">
            <div style="width:100%;">
              <n-select v-model:value="nodeForm.route_upstream_inbound_id" :options="routeLandingOptions" placeholder="沿用入站链路" />
              <div class="form-tip">选择后，本节点使用独立派生凭据，并按认证身份从共享入口转发到该落地；可重复添加节点来复用同一入口。</div>
              <n-alert v-if="nodeForm.route_upstream_broken" type="warning" :show-icon="false" style="margin-top:8px;">原固定落地已被删除，本逻辑线路已停止认证并从订阅隐藏，避免出口 IP 意外变成线路机；请重新选择落地，或明确改为沿用入站链路后保存。</n-alert>
            </div>
          </n-form-item>
          <n-form-item v-if="nodeForm.type === 'external'" label="分享链接">
            <n-input v-model:value="nodeForm.share_link" type="textarea" :rows="3" placeholder="vless://... 或订阅链接" />
          </n-form-item>
          <n-form-item label="分组">
            <n-select v-model:value="nodeForm.group_ids" :options="groupOptions" multiple placeholder="选择分组" />
          </n-form-item>
          <n-form-item label="启用"><n-switch v-model:value="nodeForm.enabled" /></n-form-item>
        </n-form>
        <n-button type="primary" block :loading="saving" @click="handleSaveNode">保存</n-button>
      </n-drawer-content>
    </n-drawer>

    <!-- 批量导入抽屉 -->
    <n-drawer v-model:show="showImport" :width="drawerW" placement="right">
      <n-drawer-content title="批量导入节点" closable>
        <n-form label-placement="left" label-width="80">
          <n-form-item label="分享链接">
            <n-input v-model:value="importLinks" type="textarea" :rows="8" placeholder="每行一个分享链接或粘贴订阅内容(base64)" />
          </n-form-item>
          <n-form-item label="分组">
            <n-select v-model:value="importGroupIds" :options="groupOptions" multiple placeholder="选择分组" />
          </n-form-item>
        </n-form>
        <n-button type="primary" block :loading="saving" @click="handleImport">导入</n-button>
      </n-drawer-content>
    </n-drawer>

    <!-- 分组编辑抽屉 -->
    <n-drawer v-model:show="showGroup" :width="drawerW" placement="right">
      <n-drawer-content :title="editingGroup ? '编辑分组' : '添加分组'" closable>
        <n-form label-placement="left" label-width="60">
          <n-form-item label="名称"><n-input v-model:value="groupForm.name" /></n-form-item>
          <n-form-item label="描述"><n-input v-model:value="groupForm.description" /></n-form-item>
          <n-form-item label="AI 路由">
            <div>
              <n-switch v-model:value="groupForm.is_ai" />
              <div class="form-tip">AI 域名会优先使用该分组内用户有权访问的节点；无需用户手动选择。</div>
            </div>
          </n-form-item>
          <n-form-item label="排序"><n-input-number v-model:value="groupForm.sort_order" :min="0" style="width:100%;" /></n-form-item>
        </n-form>
        <n-button type="primary" block :loading="saving" @click="handleSaveGroup">保存</n-button>
      </n-drawer-content>
    </n-drawer>

    <!-- 订阅源编辑抽屉 -->
    <n-drawer v-model:show="showSource" :width="drawerW" placement="right">
      <n-drawer-content :title="editingSource ? '编辑订阅源' : '添加订阅源'" closable>
        <n-form label-placement="left" label-width="60">
          <n-form-item label="名称"><n-input v-model:value="sourceForm.name" /></n-form-item>
          <n-form-item label="URL"><n-input v-model:value="sourceForm.url" placeholder="https://..." /></n-form-item>
          <n-form-item label="分组">
            <n-select v-model:value="sourceForm.group_ids" :options="groupOptions" multiple placeholder="选择分组" />
          </n-form-item>
          <n-form-item label="启用"><n-switch v-model:value="sourceForm.enabled" /></n-form-item>
        </n-form>
        <n-button type="primary" block :loading="saving" @click="handleSaveSource">保存</n-button>
      </n-drawer-content>
    </n-drawer>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import {
  NTabs, NTabPane, NDrawer, NDrawerContent, NButton, NForm, NFormItem, NInput, NInputNumber,
  NSelect, NSwitch, NRadioGroup, NRadio, NTag, NSpin, NEmpty, NCard, NModal, useMessage
} from 'naive-ui'
import { apiList, apiPost, apiPut, apiDelete } from '@/api'
import { timeAgo, fmtDateTime } from '@/utils/format'

const message = useMessage()
const tab = ref('nodes')
const loading = ref(false)
const saving = ref(false)
const reordering = ref(false)

// 抽屉宽度：移动端全屏，桌面 460px
const isMobile = ref(false)
const drawerW = computed(() => isMobile.value ? '100%' : 460)
function checkMobile() { isMobile.value = window.matchMedia('(max-width: 768px)').matches }
onMounted(() => { checkMobile(); window.addEventListener('resize', checkMobile); load() })
onUnmounted(() => window.removeEventListener('resize', checkMobile))

const nodes = ref<any[]>([])
const groups = ref<any[]>([])
const sources = ref<any[]>([])
const inbounds = ref<any[]>([])
const servers = ref<any[]>([])
const egresses = ref<any[]>([])
// 出口列表是否真的取到了。取不到时 egressById 是空的，此时不能把「查不到出口」
// 当成「出口已失效」——那会把一次加载失败画成满屏红字。
const egressesLoaded = ref(true)

const groupMap = computed(() => new Map(groups.value.map(g => [g.id, g.name])))
const inboundMap = computed(() => new Map(inbounds.value.map(i => [i.tag, i])))
const groupOptions = computed(() => groups.value.map(g => ({ label: g.name, value: g.id })))
const inboundOptions = computed(() => inbounds.value.map(i => ({ label: `${i.tag} (${i.type}:${i.listen_port})`, value: i.tag })))
const routeLandingTypes = new Set(['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria2', 'tuic'])
const routeLandingOptions = computed(() => {
  const entry = inboundMap.value.get(nodeForm.inbound_tag)
  if (entry?.type === 'mixed') return [{ label: '沿用入站原有链路（Mixed 暂不支持分流）', value: 0 }]

  const eligible = inbounds.value.filter(i =>
    i.enabled
    && chainEnabled(i.id)
    && i.id !== entry?.id
    && routeLandingTypes.has(i.type)
    && !(entry && chainReaches(i.id, entry.id)),
  )
  const byServer = new Map<number, any[]>()
  for (const inbound of eligible) {
    const serverID = inbound.server_id || 0
    const list = byServer.get(serverID) || []
    list.push(inbound)
    byServer.set(serverID, list)
  }
  // 本机排最前，远程机器沿服务器列表的现有顺序排列；异常的未知 server_id
  // 仍保留在最后，避免一个暂时缺失的服务器记录让已有落地选项凭空消失。
  const serverOrder = new Map<number, number>(servers.value.map((s, index) => [s.id, index + 1]))
  const serverIDs = [...byServer.keys()].sort((a, b) =>
    (a === 0 ? 0 : (serverOrder.get(a) ?? Number.MAX_SAFE_INTEGER))
    - (b === 0 ? 0 : (serverOrder.get(b) ?? Number.MAX_SAFE_INTEGER))
    || a - b,
  )
  return [
    { label: '沿用入站原有链路（兼容模式）', value: 0 },
    ...serverIDs.map(serverID => {
      const children = byServer.get(serverID) || []
      return {
        type: 'group' as const,
        label: `${serverName(serverID)} · ${children.length} 个入站`,
        key: `landing-server-${serverID}`,
        children: children.map(i => ({
          // 折叠后的已选值也要带机器名，不能只依赖下拉菜单里的分组标题。
          label: `${i.tag} · ${(i.type || '').toUpperCase()} @ ${serverName(serverID)}`,
          value: i.id,
        })),
      }
    }),
  ]
})

function nodeProtocol(r: any): string {
  if (r.protocol) return r.protocol.toUpperCase()
  if (r.type === 'self_built' && r.inbound_tag) {
    const ib = inboundMap.value.get(r.inbound_tag)
    if (ib) return ib.type.toUpperCase()
  }
  return '—'
}
function groupNames(ids: number[] | undefined): string {
  if (!ids || !ids.length) return '—'
  return ids.map(id => groupMap.value.get(id) || '#' + id).join(', ')
}
function inboundReuse(n: any): number {
  if (!n?.inbound_tag) return 0
  return nodes.value.filter(x => x.type === 'self_built' && x.inbound_tag === n.inbound_tag).length
}

// 统一视图：每个分组一张卡片，卡片内是该组的节点；未归任何分组的节点放到「未分组」卡片。
// 一个节点可属于多个分组，会在每个所属分组的卡片里各出现一次。
const groupedView = computed(() => {
  const out: any[] = []
  const sorted = [...groups.value].sort((a, b) => (a.sort_order || 0) - (b.sort_order || 0) || a.id - b.id)
  for (const g of sorted) {
    out.push({
      key: 'g' + g.id, name: g.name, description: g.description || '', group: g, isUngrouped: false,
      nodes: nodes.value.filter(n => (n.group_ids || []).includes(g.id)),
    })
  }
  const ungrouped = nodes.value.filter(n => !n.group_ids || n.group_ids.length === 0)
  if (ungrouped.length) {
    out.push({ key: 'ungrouped', name: '未分组', description: '', group: null, isUngrouped: true, nodes: ungrouped })
  }
  return out
})

type ReuseCatalogEntry = {
  tag: string
  protocol: string
  port: number
  serverLabel: string
  serverName: string
  groupIDs: number[]
  sourceGroups: string[]
  hasUngrouped: boolean
  count: number
}

const showReuse = ref(false)
const reuseTargetGroup = ref<any | null>(null)
const reuseSearch = ref('')
const reuseTargetName = computed(() => reuseTargetGroup.value?.name || '未分组')

// 全局入口目录：入口是物理资源，不属于某一个节点分组。只用已有自建节点暴露过的
// 入站，避免把纯落地入站误当成客户端入口；按 tag 去重并汇总其来源分组与复用数。
const reuseCatalog = computed<ReuseCatalogEntry[]>(() => {
  const byTag = new Map<string, any>()
  for (const n of nodes.value) {
    if (n.type !== 'self_built' || !n.inbound_tag) continue
    const ib = inboundMap.value.get(n.inbound_tag)
    if (!ib || !ib.enabled || !routeLandingTypes.has(ib.type)) continue
    const server = ib.server_id ? servers.value.find(s => s.id === ib.server_id) : null
    if (ib.server_id && (!server || !server.enabled)) continue
    let entry = byTag.get(n.inbound_tag)
    if (!entry) {
      entry = {
        tag: n.inbound_tag,
        protocol: (ib.type || '').toUpperCase(),
        port: ib.listen_port,
        serverLabel: serverLabel(ib.server_id),
        serverName: serverName(ib.server_id),
        groupIDs: new Set<number>(),
        sourceGroups: new Set<string>(),
        hasUngrouped: false,
        count: 0,
      }
      byTag.set(n.inbound_tag, entry)
    }
    entry.count++
    const gids = n.group_ids || []
    if (!gids.length) entry.hasUngrouped = true
    for (const gid of gids) {
      entry.groupIDs.add(gid)
      entry.sourceGroups.add(groupMap.value.get(gid) || '#' + gid)
    }
  }
  return [...byTag.values()].map(entry => ({
    ...entry,
    groupIDs: [...entry.groupIDs],
    sourceGroups: [...entry.sourceGroups],
  }))
})

function isInReuseTarget(entry: ReuseCatalogEntry): boolean {
  const gid = reuseTargetGroup.value?.id
  return gid ? entry.groupIDs.includes(gid) : entry.hasUngrouped
}
const filteredReuseCatalog = computed(() => {
  const q = reuseSearch.value.trim().toLowerCase()
  return reuseCatalog.value
    .filter(entry => !q || [entry.serverLabel, entry.serverName, entry.protocol, entry.port, entry.tag, ...entry.sourceGroups]
      .join(' ').toLowerCase().includes(q))
    .sort((a, b) => Number(isInReuseTarget(b)) - Number(isInReuseTarget(a)) || a.serverLabel.localeCompare(b.serverLabel, 'zh-CN'))
})
function openReuse(group: any | null) {
  reuseTargetGroup.value = group || null
  reuseSearch.value = ''
  showReuse.value = true
}
function chooseReuseEntry(entry: ReuseCatalogEntry) {
  editingNode.value = null
  Object.assign(nodeForm, {
    name: `${entry.serverLabel} · 新线路`, type: 'self_built', inbound_tag: entry.tag,
    route_upstream_inbound_id: 0, route_upstream_broken: false, share_link: '',
    group_ids: reuseTargetGroup.value ? [reuseTargetGroup.value.id] : [], enabled: true,
  })
  showReuse.value = false
  showNode.value = true
}

// ========== 链路拓扑 ==========
// 自建节点绑定的是某个入站，出网路径（多级中转 / 代理出口）挂在入站上，
// 因此这里按节点分组把每个节点对应入站的链路展开，与 sing-box 配置页同一套画法。
const showTopoIp = ref(false) // 默认对 IP 打码，避免截图/分享泄露真实地址
const inboundById = computed(() => new Map<number, any>(inbounds.value.map(i => [i.id, i])))
const egressById = computed(() => new Map<number, any>(egresses.value.map(e => [e.id, e])))

function serverName(id: number) { if (!id) return '本机'; const s = servers.value.find(s => s.id === id); return s ? s.name : '#' + id }
function serverLabel(id: number) {
  if (!id) return '本机'
  const s = servers.value.find(s => s.id === id)
  return s ? (s.location || s.name) : '#' + id
}

// maskHost 对 IP/域名打码：IPv4 保留首尾段，域名/IPv6 保留首尾各两位。
function maskHost(h: string): string {
  if (!h) return h
  const v4 = h.match(/^(\d{1,3})\.\d{1,3}\.\d{1,3}\.(\d{1,3})$/)
  if (v4) return `${v4[1]}.***.***.${v4[2]}`
  if (!/[.:]/.test(h)) return h
  if (h.length <= 6) return '***'
  return h.slice(0, 2) + '***' + h.slice(-2)
}

// 节点绑定的入站（仅自建节点；tag 找不到说明入站已被删除/改名）。
function inboundOfNode(n: any): any | null {
  if (!n || n.type === 'external' || !n.inbound_tag) return null
  return inboundMap.value.get(n.inbound_tag) || null
}

// 从某入站沿 upstream 链走到头，返回拓扑段列表（多级中转 + 末端出口），防环。
function chainReaches(startID: number, targetID: number): boolean {
  const seen = new Set<number>()
  let cur = inboundById.value.get(startID)
  while (cur && !seen.has(cur.id)) {
    if (cur.id === targetID) return true
    seen.add(cur.id)
    cur = cur.upstream_inbound_id ? inboundById.value.get(cur.upstream_inbound_id) : null
  }
  return false
}

function chainEnabled(startID: number): boolean {
  const seen = new Set<number>()
  let cur = inboundById.value.get(startID)
  while (cur && !seen.has(cur.id)) {
    if (!cur.enabled) return false
    seen.add(cur.id)
    if (!cur.upstream_inbound_id) return true
    cur = inboundById.value.get(cur.upstream_inbound_id)
  }
  return false
}

function chainOf(ib: any, node?: any) {
  const segs: any[] = []
  if (node?.route_upstream_broken) return [{ kind: 'route-broken' }]
  const seen = new Set<number>([ib.id])
  let cur = ib
  if (node?.route_upstream_inbound_id) {
    const selected = inboundById.value.get(node.route_upstream_inbound_id)
    if (!selected) { segs.push({ kind: 'broken-landing' }); return segs }
    seen.add(selected.id)
    segs.push({ kind: 'landing', ib: selected, off: !selected.enabled, logical: true })
    if (!selected.enabled) { segs.push({ kind: 'route-disabled' }); return segs }
    cur = selected
  }
  while (cur.upstream_inbound_id) {
    const next = inboundById.value.get(cur.upstream_inbound_id)
    // 成环和落地被删是两种完全不同的配置错误，分开报，不要都说成「落地已失效」。
    if (seen.has(next?.id)) { segs.push({ kind: 'loop' }); return segs }
    if (!next) { segs.push({ kind: 'broken-landing' }); return segs }
    seen.add(next.id)
    segs.push({ kind: 'landing', ib: next, off: !next.enabled })
    if (!next.enabled && node?.route_upstream_inbound_id) { segs.push({ kind: 'route-disabled' }); return segs }
    cur = next
  }
  // 落地被删时后端会清掉 upstream 并置 upstream_broken：链路看着是直连，
  // 实际是「本该走中转，现在从本机出网」，这行提示是它唯一的痕迹。
  if (!node?.route_upstream_inbound_id && cur.upstream_broken) segs.push({ kind: 'downgraded' })
  if (cur.egress_id) {
    const e = egressById.value.get(cur.egress_id)
    // 出口列表没加载出来时不要报「已失效」——那是个假告警。load() 会另外提示。
    if (e) segs.push({ kind: 'egress', eg: e })
    else if (egressesLoaded.value) segs.push({ kind: 'broken-egress' })
  }
  return segs
}

// 每个节点一行：kind 区分「自建且入站存在」「自建但入站失效」「外部节点」。
function topoRow(n: any) {
  if (n.type === 'external') return { node: n, ib: null, kind: 'external', segs: [] as any[] }
  const ib = inboundOfNode(n)
  if (!ib) return { node: n, ib: null, kind: 'broken', segs: [] as any[] }
  // 入站自己被停用时整行置灰：链路画得再完整，这条也是不通的。
  return { node: n, ib, kind: 'ok', off: !n.enabled || !ib.enabled, segs: chainOf(ib, n) }
}
function routeStopped(row: any): boolean {
  return (row?.segs || []).some((s: any) => s.kind === 'route-broken' || s.kind === 'route-disabled')
}
const topoGroups = computed(() => groupedView.value.map(gv => ({
  key: gv.key, name: gv.name, isUngrouped: gv.isUngrouped, rows: gv.nodes.map(topoRow),
})))

type CardRouteStep = { kind: string; role: string; label: string }
type CardRoute = { steps: CardRouteStep[]; warning: string; broken: boolean }

function routeIssue(row: any): string {
  if (row.kind === 'broken') return `入站「${row.node.inbound_tag || '未绑定'}」不存在`
  const issues: string[] = []
  for (const seg of row.segs || []) {
    switch (seg.kind) {
      case 'landing': if (seg.off) issues.push(`${serverLabel(seg.ib.server_id)}已停用`); break
      case 'downgraded': issues.push('原落地已删除，当前从入口机直连'); break
      case 'route-broken': issues.push('固定落地已删除，线路已停止'); break
      case 'route-disabled': issues.push('落地链路已停用，线路已停止'); break
      case 'loop': issues.push('链路成环，请检查中转配置'); break
      case 'broken-egress': issues.push('代理出口已失效'); break
      case 'broken-landing': issues.push('落地入站已失效'); break
    }
  }
  return issues.join(' · ')
}

function buildCardRoute(row: any): CardRoute {
  if (row.kind === 'external') return {
    steps: [
      { kind: 'external', role: '线路', label: '外部节点' },
      { kind: 'inet', role: '终点', label: '互联网' },
    ], warning: '', broken: false,
  }
  if (row.kind === 'broken') return {
    steps: [{ kind: 'broken', role: '入口', label: '入站失效' }],
    warning: routeIssue(row), broken: true,
  }

  const steps: CardRouteStep[] = [
    { kind: 'entry', role: '入口', label: serverLabel(row.ib.server_id) },
  ]
  let hardStop = false
  let downgraded = false
  for (const seg of row.segs || []) {
    if (seg.kind === 'landing') {
      steps.push({ kind: seg.off ? 'broken' : 'landing', role: seg.logical ? '固定落地' : '中转', label: serverLabel(seg.ib.server_id) })
    } else if (seg.kind === 'egress') {
      steps.push({ kind: 'egress', role: '代理出口', label: seg.eg.name })
    } else if (seg.kind === 'downgraded') {
      downgraded = true
    } else {
      hardStop = true
    }
  }
  if (!hardStop) steps.push({ kind: 'inet', role: downgraded ? '当前出口' : '终点', label: '互联网' })
  else steps.push({ kind: 'broken', role: '状态', label: '线路异常' })
  const warning = routeIssue(row)
  return { steps, warning, broken: !!warning }
}

const cardRoutes = computed(() => new Map<number, CardRoute>(
  nodes.value.map(n => [n.id, buildCardRoute(topoRow(n))]),
))
function cardRoute(n: any): CardRoute {
  return cardRoutes.value.get(n.id) || { steps: [], warning: '', broken: false }
}

// 节点落在哪台机器上——卡片上原先完全看不到这件事。链路摘要只在有中转/出口时才
// 出现，而直连节点（最常见的那种）因此一个字都不显示，要知道它在哪台机跑得先去
// sing-box 页翻入站。自建节点由入站的 server_id 决定；外部节点不在我们的机器上，
// 入站找不到时更无从谈起，两种情况都返回 null 由模板决定怎么说。
// 与 chainSummaries 同样预先算好：模板里要读多次，不该每次渲染都重查一遍入站。
const nodeServers = computed(() => {
  const out = new Map<number, string>()
  for (const n of nodes.value) {
    const ib = inboundOfNode(n)
    if (ib) out.set(n.id, serverName(ib.server_id))
  }
  return out
})
function nodeServer(n: any) { return nodeServers.value.get(n.id) || null }

// 确认「原落地已删除、现本机直连」这件事已知晓。只清提示，不动配置。
const acking = ref<number | null>(null)
async function ackUpstream(ib: any) {
  acking.value = ib.id
  try { await apiPost(`/api/admin/sb/inbounds/${ib.id}/ack-upstream`); await load() }
  catch (e: any) { message.error(e.message) }
  finally { acking.value = null }
}

// --- Nodes ---
const showNode = ref(false)
const editingNode = ref<any>(null)
// share_link must match store.Node's JSON tag — it was `link`, so the field was
// never sent: created external nodes had no link at all, and saving an existing
// one blanked its stored link.
const nodeForm = reactive({ name: '', remark: '', type: 'self_built', inbound_tag: '', route_upstream_inbound_id: 0, route_upstream_broken: false, share_link: '', group_ids: [] as number[], enabled: true })
function openNode(n?: any) {
  editingNode.value = n || null
  if (n) {
    Object.assign(nodeForm, { name: n.name, remark: n.remark || '', type: n.type || 'self_built', inbound_tag: n.inbound_tag || '', route_upstream_inbound_id: n.route_upstream_inbound_id || 0, route_upstream_broken: !!n.route_upstream_broken, share_link: n.share_link || '', group_ids: n.group_ids || [], enabled: n.enabled })
  } else {
    Object.assign(nodeForm, { name: '', remark: '', type: 'self_built', inbound_tag: '', route_upstream_inbound_id: 0, route_upstream_broken: false, share_link: '', group_ids: [], enabled: true })
  }
  showNode.value = true
}
// 从分组卡片直接添加节点，预选该分组。
function openNodeInGroup(g: any | null) {
  openNode()
  if (g) nodeForm.group_ids = [g.id]
}
async function handleSaveNode() {
  saving.value = true
  try {
    if (editingNode.value) await apiPut(`/api/admin/nodes/${editingNode.value.id}`, nodeForm)
    else await apiPost('/api/admin/nodes', nodeForm)
    message.success('保存成功'); showNode.value = false; await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}
async function handleDeleteNode(id: number) {
  try { await apiDelete(`/api/admin/nodes/${id}`); message.success('已删除'); await load() } catch (e: any) { message.error(e.message) }
}

// 调整节点在分组内（及订阅/列表中）的顺序。节点按全局 sort_order 排，分组只是按
// 归属过滤，因此「组内前移/后移」= 把该节点与组内相邻节点在全局数组里对调位置，
// 再把完整 id 顺序提交后端。乐观更新，失败回滚重载。
async function moveNodeInGroup(gv: any, idx: number, dir: -1 | 1) {
  const target = idx + dir
  if (target < 0 || target >= gv.nodes.length || reordering.value) return
  const aId = gv.nodes[idx].id
  const bId = gv.nodes[target].id
  const arr = [...nodes.value]
  const gi = arr.findIndex(n => n.id === aId)
  const gj = arr.findIndex(n => n.id === bId)
  if (gi < 0 || gj < 0) return
  ;[arr[gi], arr[gj]] = [arr[gj], arr[gi]]
  nodes.value = arr
  reordering.value = true
  try {
    await apiPost('/api/admin/nodes/reorder', { ids: arr.map(n => n.id) })
  } catch (e: any) {
    message.error(e.message || '排序失败'); await load()
  } finally { reordering.value = false }
}

// --- Bulk import ---
const showImport = ref(false)
const importLinks = ref('')
const importGroupIds = ref<number[]>([])
function openNodeImport() { importLinks.value = ''; importGroupIds.value = []; showImport.value = true }
async function handleImport() {
  saving.value = true
  try {
    await apiPost('/api/admin/nodes/import', { links: importLinks.value, group_ids: importGroupIds.value })
    message.success('导入成功'); showImport.value = false; await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// --- Groups ---
const showGroup = ref(false)
const editingGroup = ref<any>(null)
const groupForm = reactive({ name: '', description: '', is_ai: false, sort_order: 0 })
function openGroup(g?: any) {
  editingGroup.value = g || null
  if (g) Object.assign(groupForm, { name: g.name, description: g.description || '', is_ai: !!g.is_ai, sort_order: g.sort_order || 0 })
  else Object.assign(groupForm, { name: '', description: '', is_ai: false, sort_order: 0 })
  showGroup.value = true
}
async function handleSaveGroup() {
  saving.value = true
  try {
    if (editingGroup.value) await apiPut(`/api/admin/node-groups/${editingGroup.value.id}`, groupForm)
    else await apiPost('/api/admin/node-groups', groupForm)
    message.success('保存成功'); showGroup.value = false; await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}
async function handleDeleteGroup(id: number) {
  try { await apiDelete(`/api/admin/node-groups/${id}`); message.success('已删除'); await load() } catch (e: any) { message.error(e.message) }
}

// --- Sources ---
const showSource = ref(false)
const editingSource = ref<any>(null)
const sourceForm = reactive({ name: '', url: '', group_ids: [] as number[], enabled: true })
function openSource(s?: any) {
  editingSource.value = s || null
  if (s) Object.assign(sourceForm, { name: s.name, url: s.url, group_ids: s.group_ids || [], enabled: s.enabled })
  else Object.assign(sourceForm, { name: '', url: '', group_ids: [], enabled: true })
  showSource.value = true
}
async function handleSaveSource() {
  saving.value = true
  try {
    if (editingSource.value) await apiPut(`/api/admin/node-sources/${editingSource.value.id}`, sourceForm)
    else await apiPost('/api/admin/node-sources', sourceForm)
    message.success('保存成功'); showSource.value = false; await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}
async function handleFetchSource(id: number) {
  try { await apiPost(`/api/admin/node-sources/${id}/fetch`); message.success('拉取成功'); await load() } catch (e: any) { message.error(e.message) }
}
async function handleDeleteSource(id: number) {
  try { await apiDelete(`/api/admin/node-sources/${id}`); message.success('已删除'); await load() } catch (e: any) { message.error(e.message) }
}

async function load() {
  loading.value = true
  // 服务器/出口只用于链路拓扑的展示，取不到时降级为空数组，不影响节点管理主流程。
  // 但降级要说出来：出口列表为空会让每条带出口的链路都显示成「出口已失效」，
  // 那是个假告警——拿不到就别画，并明确告诉管理员拓扑是不完整的。
  let degraded = ''
  try {
    const [n, g, s, i, sv, eg] = await Promise.all([
      apiList('/api/admin/nodes'), apiList('/api/admin/node-groups'),
      apiList('/api/admin/node-sources'), apiList('/api/admin/inbounds'),
      apiList('/api/admin/servers').catch(() => { degraded = '服务器列表'; return [] }),
      apiList('/api/admin/sb/egresses').catch(() => { degraded = degraded ? degraded + '、代理出口' : '代理出口'; return [] }),
    ])
    nodes.value = n; groups.value = g; sources.value = s; inbounds.value = i
    servers.value = sv; egresses.value = eg
    egressesLoaded.value = !degraded.includes('代理出口')
    if (degraded) message.warning(`${degraded}加载失败，链路拓扑显示不完整`)
  } catch {} finally { loading.value = false }
}
</script>

<style scoped>
/* 拉取失败要能一眼看到：这条源现在等于没有节点，用户订阅里也少了这一批 */
.src-err {
  font-size: 12px; color: var(--danger); background: var(--danger-soft, #fdf2f2);
  border-radius: 6px; padding: 6px 8px; line-height: 1.5;
  overflow: hidden; text-overflow: ellipsis; display: -webkit-box;
  -webkit-line-clamp: 2; -webkit-box-orient: vertical;
}
:deep(.n-drawer-content-body) { display: flex; flex-direction: column; }
.group-card { margin-bottom: 16px; border-radius: 12px; }
.group-card :deep(.n-card-header) { padding-bottom: 10px; }
.group-actions { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.group-desc { color: var(--text-2); font-size: 12px; margin: -2px 0 12px; }
.form-tip { font-size: 12px; color: var(--text-3, #999); margin-top: 5px; line-height: 1.5; }

/* 全局入口选择器：先说明目标分组，再按物理入口选择，不伪装成“复制节点”。 */
.reuse-intro { margin-bottom: 13px; padding: 11px 12px; border-radius: 9px; background: var(--bg-soft); color: var(--text-2); font-size: 12px; }
.reuse-intro > span { margin-right: 7px; color: var(--text-3); }
.reuse-intro > b { color: var(--text); font-size: 13px; }
.reuse-intro p { margin: 5px 0 0; line-height: 1.5; }
.reuse-list { display: flex; flex-direction: column; gap: 8px; max-height: 430px; margin-top: 12px; overflow-y: auto; }
.reuse-entry {
  display: flex; align-items: center; gap: 12px; width: 100%; padding: 11px 12px;
  border: 1px solid var(--border); border-radius: 10px; background: var(--card);
  color: inherit; text-align: left; font: inherit; cursor: pointer;
  transition: border-color .18s ease, background .18s ease, transform .18s ease;
}
.reuse-entry:hover { border-color: rgba(32, 128, 240, .38); background: rgba(32, 128, 240, .035); transform: translateY(-1px); }
.reuse-entry-main { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: 3px; }
.reuse-entry-head { display: flex; align-items: center; gap: 7px; min-width: 0; }
.reuse-entry-head > b { overflow: hidden; color: var(--text); font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
.reuse-proto, .reuse-current { flex: none; padding: 1px 6px; border-radius: 999px; font-size: 10px; font-weight: 600; }
.reuse-proto { background: rgba(32, 128, 240, .09); color: #2080f0; }
.reuse-current { background: rgba(24, 160, 88, .1); color: #168a4c; }
.reuse-machine { overflow: hidden; color: var(--text-2); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.reuse-tag { overflow: hidden; color: var(--text-3); font: 10.5px/1.35 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; text-overflow: ellipsis; white-space: nowrap; }
.reuse-groups { color: var(--text-3); font-size: 10.5px; }
.reuse-pick { display: inline-flex; flex: none; align-items: center; gap: 5px; color: #2080f0; font-size: 12px; font-weight: 600; }
.reuse-pick i { font-style: normal; transition: transform .18s ease; }
.reuse-entry:hover .reuse-pick i { transform: translateX(2px); }

/* 节点卡片：链路是主体，操作退到卡片底部。 */
.list-card { gap: 9px; border-radius: 10px; }
.route-preview {
  margin-top: 1px; padding: 10px 10px 8px; border-radius: 9px;
  background: rgba(32, 128, 240, 0.045); border: 1px solid rgba(32, 128, 240, 0.1);
}
.route-track { display: flex; align-items: center; min-width: 0; overflow-x: auto; scrollbar-width: none; }
.route-track::-webkit-scrollbar { display: none; }
.route-step { display: flex; align-items: center; gap: 7px; min-width: 0; flex: 1 1 78px; color: var(--text-2); }
.route-step:last-child { flex: 0 1 76px; }
.route-dot { width: 8px; height: 8px; flex: 0 0 8px; border-radius: 50%; background: #2080f0; box-shadow: 0 0 0 3px rgba(32, 128, 240, 0.12); }
.route-step.landing .route-dot { background: #f0a020; box-shadow: 0 0 0 3px rgba(240, 160, 32, 0.13); }
.route-step.egress .route-dot { background: #7c3aed; box-shadow: 0 0 0 3px rgba(124, 58, 237, 0.12); }
.route-step.external .route-dot { background: #8b5cf6; box-shadow: 0 0 0 3px rgba(139, 92, 246, 0.12); }
.route-step.inet .route-dot { background: #18a058; box-shadow: 0 0 0 3px rgba(24, 160, 88, 0.12); }
.route-step.broken .route-dot { background: #d03050; box-shadow: 0 0 0 3px rgba(208, 48, 80, 0.12); }
.route-copy { display: flex; flex-direction: column; min-width: 0; line-height: 1.2; }
.route-copy small { color: var(--text-3, #999); font-size: 10px; font-weight: 500; }
.route-copy b { color: var(--text); font-size: 11.5px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.route-edge { display: flex; align-items: center; flex: 0 0 22px; margin: 0 5px; color: var(--text-3, #aeb4bd); }
.route-edge i { width: 100%; height: 1px; background: currentColor; position: relative; }
.route-edge i::after { content: ''; position: absolute; right: -1px; top: -3px; width: 5px; height: 5px; border-top: 1px solid currentColor; border-right: 1px solid currentColor; transform: rotate(45deg); }
.route-warning { margin-top: 8px; padding-top: 7px; border-top: 1px solid rgba(208, 48, 80, 0.12); color: #d03050; font-size: 11px; line-height: 1.4; }
.route-preview.warn { background: rgba(208, 48, 80, 0.035); border-color: rgba(208, 48, 80, 0.13); }
.list-card .lc-foot { justify-content: space-between; gap: 8px; margin-top: 0; padding-top: 2px; }
.order-actions, .node-actions { display: inline-flex; align-items: center; gap: 3px; }
.node-actions { margin-left: auto; }

/* 完整拓扑：节点块 + 连接线，避免一整行彩色标签和文字箭头。 */
.topo { display: flex; flex-direction: column; gap: 18px; padding: 4px 2px; }
.topo-legend { display: flex; flex-wrap: wrap; gap: 16px; padding: 0 2px; font-size: 12px; color: var(--text-3, #888); }
.topo-legend span { display: inline-flex; align-items: center; gap: 5px; }
.topo-legend .dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
.dot.client { background: #909399; }
.dot.entry { background: #2080f0; }
.dot.landing { background: #f0a020; }
.dot.egress { background: #7c3aed; }
.dot.inet { background: #18a058; }
.topo-machine { border: 1px solid var(--n-border-color, #e6e6ec); border-radius: 12px; padding: 14px; background: var(--card, #fff); }
.topo-mhead { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.topo-row { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; padding: 11px; border-radius: 10px; background: rgba(128, 128, 128, 0.035); }
.topo-row + .topo-row { margin-top: 8px; }
.topo-row.off { opacity: 0.42; }
.topo-node { display: inline-flex; flex-direction: column; align-items: flex-start; justify-content: center; gap: 2px; min-height: 44px; max-width: 260px; padding: 7px 10px; border-radius: 9px; font-size: 12px; border: 1px solid transparent; white-space: nowrap; }
.topo-node small { color: var(--text-3, #999); font-size: 9.5px; line-height: 1; }
.topo-node.client { min-width: 68px; background: rgba(144, 147, 153, 0.08); border-color: rgba(144, 147, 153, 0.16); color: var(--text-2, #666); }
.topo-node.entry { background: rgba(32, 128, 240, 0.07); border-color: rgba(32, 128, 240, 0.2); }
.topo-node.landing { background: rgba(240, 160, 32, 0.08); border-color: rgba(240, 160, 32, 0.23); }
.topo-node.egress { background: rgba(124, 58, 237, 0.07); border-color: rgba(124, 58, 237, 0.2); }
.topo-node.inet { min-width: 68px; background: rgba(24, 160, 88, 0.07); border-color: rgba(24, 160, 88, 0.18); color: #168a4c; }
.topo-node.inet.stopped { background: rgba(208, 48, 80, 0.06); border-color: rgba(208, 48, 80, 0.18); color: #d03050; }
.topo-node b { max-width: 210px; overflow: hidden; text-overflow: ellipsis; font-weight: 650; }
.topo-main, .topo-detail { display: flex; align-items: baseline; gap: 6px; max-width: 100%; }
.topo-proto { font-size: 11px; opacity: 0.75; }
.topo-port, .topo-loc { font-size: 11px; opacity: 0.6; }
.topo-arrow { position: relative; display: inline-flex; align-items: center; justify-content: center; width: 38px; height: 28px; color: var(--text-3, #aeb4bd); user-select: none; }
.topo-arrow i { width: 100%; border-top: 1px solid currentColor; position: relative; }
.topo-arrow i::after { content: ''; position: absolute; right: 0; top: -3px; width: 5px; height: 5px; border-top: 1px solid currentColor; border-right: 1px solid currentColor; transform: rotate(45deg); }
.topo-arrow em { position: absolute; top: -1px; padding: 0 3px; background: var(--card, #fff); font-size: 9px; font-style: normal; font-weight: 500; white-space: nowrap; }
.topo-arrow.relay { width: 58px; color: #d98b12; }
.topo-arrow.egress { width: 64px; color: #7c3aed; }
.topo-arrow.relay.warn { color: #d03050; }
.topo-actions { margin-left: auto; display: inline-flex; align-items: center; gap: 4px; padding-left: 8px; }
.topo-tip { font-size: 12px; color: var(--text-3, #999); line-height: 1.6; margin: 0; }
.machine-name { font-weight: 650; font-size: 15px; color: var(--text); }
@media (max-width: 600px) {
  .reuse-entry { align-items: flex-start; }
  .reuse-entry-head { flex-wrap: wrap; }
  .reuse-pick { padding-top: 2px; }
  .route-edge { flex-basis: 14px; margin: 0 3px; }
  .topo-actions { width: 100%; margin-left: 0; padding: 6px 0 0; justify-content: flex-end; }
  .node-actions { gap: 1px; }
}
</style>

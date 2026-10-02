<template>
  <div>
    <div class="page-head">
      <div><h2 class="page-title">服务器管理</h2><p class="page-sub">远程机器、SSH 接管、探针与 sing-box 运行版本</p></div>
      <div class="page-actions"><n-button type="primary" @click="openForm()">添加服务器</n-button></div>
    </div>
    <div class="resource-overview">
      <div class="resource-metric"><b>{{ servers.length }}</b><span>全部远程服务器</span></div>
      <div class="resource-metric success"><b>{{ servers.filter(s => s.enabled).length }}</b><span>已启用</span></div>
      <div class="resource-metric"><b>{{ servers.filter(s => s.probe_enabled).length }}</b><span>探针已开启</span></div>
      <div class="resource-metric" :class="{ danger: versions.filter(v => v.too_old || (v.version && !v.has_v2ray_api)).length }"><b>{{ versions.filter(v => v.too_old || (v.version && !v.has_v2ray_api)).length }}</b><span>版本或统计异常</span></div>
    </div>
    <AdminRelayMetering />
    <!-- 各节点实际在跑的 sing-box。数据来自面板本来就在做的能力探测，
         以前只取了「有没有 v2ray_api」，版本号被丢掉了。 -->
    <n-card title="节点 sing-box 版本" size="small" style="margin-bottom:16px;">
      <template #header-extra>
        <n-space size="small">
          <n-button size="tiny" :loading="verLoading" @click="refreshVersions">重新检测</n-button>
        </n-space>
      </template>
      <p class="nv-note">
        一键脚本装完之后，版本号只印在当时的终端里。这里长期显示每台机器实际在跑的版本 ——
        数据来自面板每轮下发配置时本来就会做的探测，不额外连一次机器。
        面板生成的配置要求 <b>sing-box ≥ {{ minSupported }}</b>；低于这个版本，节点的
        <code>sing-box check</code> 会失败，面板会<b>停止向它下发任何配置</b>（旧配置继续跑，所以表面看不出来）。
        「重装」装的是<b>面板自己发布的构建</b>（随面板版本走，含流量统计所需的 <code>v2ray_api</code>），
        不是 sing-box 官方发布版 —— 官方版不带这个插件，装上去流量就统计不到了。
      </p>
      <div v-if="versions.length" class="nv-list">
        <div v-for="n in versions" :key="n.server_id" class="nv-row">
          <div class="nv-main">
            <span class="nv-name">{{ n.name }}<span v-if="n.host" class="nv-host">{{ n.host }}</span></span>
            <span class="nv-ver">{{ n.version || '—' }}</span>
            <n-tag v-if="n.too_old" type="error" size="tiny" :bordered="false">版本过低</n-tag>
            <n-tag v-else-if="!n.version" type="default" size="tiny" :bordered="false">未知</n-tag>
            <n-tag v-if="n.version && !n.has_v2ray_api" type="error" size="tiny" :bordered="false">缺少 with_v2ray_api</n-tag>
          </div>
          <div class="nv-side">
            <span v-if="n.checked_at" class="nv-time">{{ fmtDateTime(n.checked_at) }}</span>
            <n-button size="tiny" :disabled="!n.upgradable || n.upgrading" :loading="n.upgrading"
                      @click="confirmUpgrade(n)">
              {{ n.upgrading ? '安装中' : (n.version ? '重装' : '安装') }}
            </n-button>
          </div>
          <!-- 安装是后台跑的（要往节点上拉 ~60MB 的二进制），这里显示脚本的输出。 -->
          <div v-if="n.upgrading" class="nv-err" style="color:var(--text-3);">
            正在安装…（往节点下载 sing-box 约 60MB，通常 1–3 分钟；离开本页不影响安装）
          </div>
          <div v-else-if="n.upgrade_error" class="nv-err">
            安装失败：<pre class="nv-out">{{ n.upgrade_error }}</pre>
          </div>
          <div v-if="n.error" class="nv-err">探测失败：{{ n.error }}</div>
          <div v-else-if="n.version && !n.has_v2ray_api" class="nv-err">
            该版本不含 <code>with_v2ray_api</code> 插件 —— 这台机器的流量<b>统计不到</b>，配额也不会生效（界面上一直显示 0）。
            点「重装」换成面板发布的版本即可；官方发布版不带这个插件。
          </div>
        </div>
      </div>
      <n-empty v-else-if="!verLoading" description="暂无数据，点「重新检测」" style="padding:20px 0;" />
    </n-card>

    <n-spin :show="loading">
      <div v-if="servers.length" class="card-grid">
        <div v-for="(s, idx) in servers" :key="s.id" class="list-card">
          <div class="lc-head">
            <span class="lc-title"><i class="order-no">{{ idx + 1 }}</i>{{ s.name || '—' }}</span>
            <span class="lc-order">
              <n-button size="tiny" quaternary circle :disabled="idx === 0 || reordering" title="前移" @click="moveServer(idx, -1)">←</n-button>
              <n-button size="tiny" quaternary circle :disabled="idx === servers.length - 1 || reordering" title="后移" @click="moveServer(idx, 1)">→</n-button>
              <n-tag :type="s.enabled ? 'success' : 'default'" size="small" :bordered="false">{{ s.enabled ? '启用' : '禁用' }}</n-tag>
            </span>
          </div>
          <div class="lc-meta">
            <span class="kv">主机 <b>{{ s.host }}</b></span>
            <span class="kv">端口 <b>{{ s.port }}</b></span>
            <span class="kv">探针 <n-tag :type="s.probe_enabled ? 'info' : 'default'" size="tiny" :bordered="false">{{ s.probe_enabled ? '开' : '关' }}</n-tag></span>
          </div>
          <div class="lc-foot">
            <n-button size="tiny" type="primary" secondary @click="openTrafficAnalysis(s)">流量分析</n-button>
            <n-button size="tiny" @click="handleRebuild(s.id)">重建</n-button>
            <n-button size="tiny" @click="openForm(s)">编辑</n-button>
            <n-button size="tiny" :loading="clearing === s.id" @click="handleClearHostKey(s)">清除密钥指纹</n-button>
            <n-button size="tiny" type="error" @click="handleDelete(s.id)">删除</n-button>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="下一步：添加服务器，或在面板本机安装 sing-box 后重新检测" style="padding:40px 0;"><template #extra><n-button @click="openForm()">添加服务器</n-button></template></n-empty>
    </n-spin>

    <n-modal v-model:show="showForm" preset="card" :title="editing?'编辑服务器':'添加服务器'" style="max-width:560px;">
      <n-form label-placement="left" label-width="100">
        <n-form-item label="名称"><n-input v-model:value="form.name" /></n-form-item>
        <n-form-item label="主机"><n-input v-model:value="form.host" placeholder="IP 或域名" /></n-form-item>
        <n-form-item label="SSH 端口"><n-input-number v-model:value="form.port" :min="1" :max="65535" style="width:100%;" /></n-form-item>
        <n-form-item label="SSH 用户"><n-input v-model:value="form.ssh_user" placeholder="root" @update:value="onSshUserInput" /></n-form-item>
        <n-form-item label="sudo 提权">
          <div style="width:100%;">
            <n-switch v-model:value="form.use_sudo" />
            <div class="hint">
              非 root 账号必须打开：写配置、装配置、重启服务都要 root。
              <b>需要免密 sudo（NOPASSWD），等同于把 root 交给面板</b>；填了密码则用密码提权。
            </div>
          </div>
        </n-form-item>
        <n-form-item v-if="form.use_sudo" label="sudo 密码"><n-input v-model:value="form.sudo_password" type="password" show-password-on="click" placeholder="留空并保存 = 改用免密 sudo" /></n-form-item>
        <n-form-item label="SSH 密码"><n-input v-model:value="form.ssh_password" type="password" show-password-on="click" placeholder="与密钥二选一" /></n-form-item>
        <n-form-item label="SSH 私钥">
          <div style="width:100%;">
            <n-radio-group v-model:value="keyMode" size="small">
              <n-radio-button value="paste">粘贴</n-radio-button>
              <n-radio-button value="file">从文件选择</n-radio-button>
            </n-radio-group>
            <n-input v-if="keyMode==='paste'" v-model:value="form.ssh_key" type="textarea" :rows="3"
                     placeholder="-----BEGIN ... PRIVATE KEY-----" style="margin-top:8px;" />
            <template v-else>
              <n-select v-model:value="form.ssh_key_path" :options="keyOptions" clearable
                        :loading="keysLoading" placeholder="选择一个私钥文件" style="margin-top:8px;" />
              <div class="hint">
                目录：<code>{{ keyDir || '未配置' }}</code>
                <template v-if="!keysConfigured"> —— 未配置，设置 <code>QZ_SSH_KEY_DIR</code> 后可用。</template>
                <template v-else-if="!keyFiles.length"> —— 目录不存在或还没有私钥文件；把私钥放进去并 <code>chmod 600</code>。</template>
                <br>私钥不经过浏览器、也不入库。容器内面板以 uid 10001 运行，挂进去的密钥要它能读（<code>chown 10001</code>）。
                <template v-if="form.ssh_key"><br><b>已选文件时，上面粘贴过的私钥不再使用。</b></template>
              </div>
            </template>
          </div>
        </n-form-item>
        <n-form-item label="密钥密码"><n-input v-model:value="form.ssh_key_pass" type="password" show-password-on="click" placeholder="已保存时显示 ***；清空并保存可删除" /></n-form-item>
        <n-form-item label="配置路径"><n-input v-model:value="form.config_path" placeholder="/etc/sing-box/config.json" /></n-form-item>
        <n-form-item label="systemd 单元"><n-input v-model:value="form.systemd_unit" placeholder="sing-box" /></n-form-item>
        <n-form-item label="sing-box 路径"><n-input v-model:value="form.sing_box_bin" placeholder="/usr/local/bin/sing-box" /></n-form-item>
        <n-form-item label="v2ray 监听"><n-input v-model:value="form.v2ray_listen" placeholder="127.0.0.1:18080" /></n-form-item>
        <n-form-item label="启用"><n-switch v-model:value="form.enabled" /></n-form-item>
      </n-form>
      <n-space>
        <n-button type="primary" :loading="saving" @click="handleSave">保存</n-button>
        <n-button @click="handleTest" :loading="testing">测试连接</n-button>
      </n-space>
    </n-modal>

    <n-drawer v-model:show="showTraffic" :width="trafficDrawerWidth" placement="right" @after-leave="disposeTrafficChart">
      <n-drawer-content :title="`周期流量分析${trafficAnalysis?.server_name ? ' · ' + trafficAnalysis.server_name : ''}`" closable>
        <n-spin :show="trafficLoading">
          <template v-if="trafficAnalysis">
            <n-alert v-if="trafficIncomplete" type="warning" :bordered="false" style="margin-bottom:14px;">
              当前周期的设备流量仅从 {{ fmtDateTime(trafficAnalysis.usage.coverage_start) }} 开始采集；未校准前，周期总量和容量预估可能偏低。
            </n-alert>
            <div class="traffic-summary-grid">
              <div class="traffic-summary-card"><span>本周期已用</span><b>{{ fmtBytes(trafficAnalysis.usage.total) }}</b><small>{{ trafficModeLabel(trafficAnalysis.accounting_mode) }}</small></div>
              <div class="traffic-summary-card"><span>剩余流量</span><b>{{ trafficAnalysis.limit_bytes > 0 ? fmtBytes(trafficRemaining) : '未设上限' }}</b><small>重置 {{ fmtDateTime(trafficAnalysis.next_reset) }}</small></div>
              <div class="traffic-summary-card"><span>近期日均</span><b>{{ trafficAnalysis.projection.available ? fmtBytes(trafficAnalysis.projection.daily_rate_bytes) : '—' }}</b><small>{{ trafficAnalysis.projection.available ? `基于 ${trafficAnalysis.projection.sample_days.toFixed(1)} 天` : trafficAnalysis.projection.reason }}</small></div>
              <div class="traffic-summary-card capacity"><span>预计还能新增</span><b>{{ trafficAnalysis.projection.available ? trafficAnalysis.projection.estimated_additional_users + ' 人' : '—' }}</b><small>保证现有活跃用户用到本周期结束</small></div>
            </div>

            <div v-if="trafficAnalysis.limit_bytes > 0" class="quota-overview">
              <div><span>周期额度消耗</span><b>{{ trafficPercent.toFixed(1) }}%</b></div>
              <n-progress type="line" :percentage="Math.min(trafficPercent, 100)" :show-indicator="false" :height="8" :color="trafficPercent >= 90 ? '#c2685c' : trafficPercent >= 70 ? '#bf9540' : '#5c8b70'" />
              <p v-if="trafficAnalysis.projection.available">
                按近期速度，周期结束预计使用 {{ fmtBytes(trafficAnalysis.projection.projected_cycle_total_bytes) }}。
                <template v-if="trafficAnalysis.projection.estimated_exhaustion_at && trafficAnalysis.projection.estimated_exhaustion_at < trafficAnalysis.next_reset">预计 {{ fmtDateTime(trafficAnalysis.projection.estimated_exhaustion_at) }} 用尽额度。</template>
              </p>
            </div>

            <section class="traffic-section">
              <div class="traffic-section-head"><div><h3>周期流量趋势</h3><p>物理网卡 IN / OUT 按日统计；折线是当前服务商计费口径下的日用量。</p></div></div>
              <div ref="trafficChartEl" class="traffic-chart" />
              <n-empty v-if="!trafficAnalysis.daily?.length" description="本周期暂无可绘制的探针数据" style="padding:24px 0;" />
            </section>

            <AdminServiceTraffic v-if="trafficAnalysis.service" :service="trafficAnalysis.service" />
            <section v-else class="traffic-section">
              <div class="traffic-section-head">
                <div><h3>流量消耗来源</h3><p>来自本机 sing-box 的用户级统计，不包含系统更新、SSH 等非代理流量。</p></div>
                <span v-if="trafficAnalysis.attribution.coverage_start" class="coverage-chip">自 {{ fmtDateTime(trafficAnalysis.attribution.coverage_start) }} 归因</span>
              </div>
              <div class="source-overview">
                <span><b>{{ trafficAnalysis.attribution.active_users }}</b> 位活跃用户</span>
                <span>已归因 <b>{{ fmtBytes(trafficAnalysis.attribution.total) }}</b></span>
                <span v-if="trafficAnalysis.projection.available">人均日消耗 <b>{{ fmtBytes(trafficAnalysis.projection.per_user_daily_bytes) }}</b></span>
              </div>
              <div v-if="trafficAnalysis.attribution.sources?.length" class="traffic-sources">
                <div v-for="(source, idx) in trafficAnalysis.attribution.sources" :key="source.user_id" class="traffic-source-row">
                  <span class="source-rank">{{ idx + 1 }}</span>
                  <div class="source-main">
                    <div><b>{{ source.username }}</b><span>{{ fmtBytes(source.total) }}</span></div>
                    <div class="source-track"><i :style="{ width: sourcePercent(source.total) + '%' }" /></div>
                    <small>上行 {{ fmtBytes(source.up) }} · 下行 {{ fmtBytes(source.down) }}</small>
                  </div>
                </div>
              </div>
              <n-empty v-else description="尚无按服务器归因的用户流量；升级后会从下一次统计轮询开始积累" style="padding:28px 0;" />
            </section>

            <n-alert type="info" :bordered="false" style="margin-top:14px;">
              “预计还能新增”不是人数硬上限：它用归因窗口内的物理流量 ÷ 活跃用户得到人均日消耗，先预留现有用户到重置日的预计用量，再计算剩余额度可承载的同等用户数。归因不足 6 小时时不出数。
            </n-alert>
          </template>
        </n-spin>
      </n-drawer-content>
    </n-drawer>
  </div>
</template>
<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, nextTick, shallowRef, h } from 'vue'
import { NSpin, NButton, NModal, NForm, NFormItem, NInput, NInputNumber, NSwitch, NSpace, NTag, NEmpty, NCard, NSelect, NRadioGroup, NRadioButton, NDrawer, NDrawerContent, NAlert, NProgress, useMessage, useDialog } from 'naive-ui'
import { apiList, apiPost, apiPut, apiDelete, apiGet } from '@/api'
import { fmtBytes, fmtDateTime, pct } from '@/utils/format'
import AdminServiceTraffic from '@/components/AdminServiceTraffic.vue'
import AdminRelayMetering from '@/components/AdminRelayMetering.vue'
import * as echarts from 'echarts'
const message = useMessage()
const dialog = useDialog()
const servers = ref<any[]>([])
const loading = ref(false); const saving = ref(false); const testing = ref(false)
const reordering = ref(false)
const showForm = ref(false); const editing = ref<any>(null)
const mk = () => ({ name:'', host:'', port:22, ssh_user:'root', ssh_password:'', ssh_key:'', ssh_key_pass:'', use_sudo:false, sudo_password:'', ssh_key_path:'', config_path:'/etc/sing-box/config.json', systemd_unit:'sing-box', sing_box_bin:'/usr/local/bin/sing-box', v2ray_listen:'127.0.0.1:18080', enabled:true })
const form = reactive(mk())

// 私钥来源二选一。粘贴走 ssh_key（加密入库），选文件走 ssh_key_path（只存文件名，
// 内容始终留在面板机器上）。后端以文件优先。
const keyMode = ref<'paste'|'file'>('paste')
const keyFiles = ref<any[]>([])
const keyDir = ref(''); const keysConfigured = ref(false); const keysLoading = ref(false)
const keyOptions = computed(() => keyFiles.value.map((f:any) => ({
  // 可读性直接标在选项上：容器内以 uid 10001 跑，宿主机 chmod 600 的 root 私钥
  // 挂进去读不了，是这个功能最常见的失败方式。让人一眼看见，别保存完去猜。
  label: f.name + (!f.readable ? '（面板读不了）' : (!f.mode_ok ? '（权限过松）' : '')),
  value: f.name,
  disabled: !f.readable,
})))
async function loadKeys(){
  keysLoading.value = true
  try{
    const r:any = await apiGet('/api/admin/ssh-keys')
    keyFiles.value = r?.files || []; keyDir.value = r?.dir || ''; keysConfigured.value = !!r?.configured
  }catch{ keyFiles.value = []; keysConfigured.value = false }
  finally{ keysLoading.value = false }
}
function openForm(s?:any){
  editing.value = s||null
  if(s){ Object.assign(form,{ name:s.name, host:s.host, port:s.port||22, ssh_user:s.ssh_user||'root', ssh_password:'', ssh_key:'', ssh_key_pass:s.ssh_key_pass||'', use_sudo:s.use_sudo??false, sudo_password:s.sudo_password||'', ssh_key_path:s.ssh_key_path||'', config_path:s.config_path||'/etc/sing-box/config.json', systemd_unit:s.systemd_unit||'sing-box', sing_box_bin:s.sing_box_bin||'/usr/local/bin/sing-box', v2ray_listen:s.v2ray_listen||'127.0.0.1:18080', enabled:s.enabled??true }) }
  else { Object.assign(form, mk()) }
  keyMode.value = form.ssh_key_path ? 'file' : 'paste'
  showForm.value = true
  loadKeys()
}
// 改 SSH 用户时替管理员把 sudo 开关拨到通常正确的位置：非 root 必然要提权，
// root 必然不要。只在「人手动改这个输入框」时触发 —— 打开已有服务器走的是
// openForm，不经过这里，所以不会覆盖管理员自己拨过的开关。
function onSshUserInput(v:string){
  form.use_sudo = (v||'').trim() !== 'root' && (v||'').trim() !== ''
}

async function handleSave(){
  saving.value = true
  try{
    const b:any = {...form}
    if(!b.ssh_password) delete b.ssh_password
    if(!b.ssh_key) delete b.ssh_key
    // 这两个字段必须把空字符串送到后端：更新接口以「字段缺失」表示保留旧值，
    // 以空字符串表示真的清除。编辑时保留的 *** 会由后端换回原密文。
    // sudo 已关闭时密码不再有用途，保存时顺手从库里清掉。
    if(!b.use_sudo) b.sudo_password = ''
    // 切回粘贴就是明确不要文件了，所以显式送空把它清掉（更新接口是「没送的字段
    // 保留原值」，delete 会让旧的文件名留在库里继续生效）。反过来选了文件时不动
    // ssh_key：后端本来就文件优先，而空的 ssh_key 会被下面删掉以免误清密钥。
    if(keyMode.value === 'paste'){ b.ssh_key_path = '' }
    else { delete b.ssh_key }
    if(editing.value) await apiPut(`/api/admin/servers/${editing.value.id}`,b)
    else await apiPost('/api/admin/servers',b)
    message.success('保存成功'); showForm.value=false; editing.value=null; await load()
  }catch(e:any){message.error(e.message)}finally{saving.value=false}
}
async function handleTest(){ testing.value=true; try{ const id=editing.value?.id; if(!id){message.warning('请先保存');return} await apiPost(`/api/admin/servers/${id}/test`); message.success('连接成功') }catch(e:any){message.error(e.message)}finally{testing.value=false} }
async function handleRebuild(id:number){ try{await apiPost(`/api/admin/servers/${id}/rebuild`);message.success('重建成功')}catch(e:any){message.error(e.message)} }

// 清除固定的 SSH 主机密钥并重新信任。
// 面板首次连上一台机器时会记住它的主机密钥，此后每次连接都要求对得上——这是拦住
// 「有人冒充这个 IP 骗走 root 凭据」的那道门。但机器重装系统会重新生成主机密钥，
// 换机复用同一条记录也一样，那时面板就会一直拒连（表现为配置下发失败），而这道门
// 只能从这里打开：host_key 不下发给前端，编辑服务器也不会动它。
// 所以按钮必须带确认：清除等于放弃对这台机器身份的既有判断。
const clearing = ref<number|null>(null)
// 弹窗正文用 pre-line 渲染：naive 的 dialog 把 content 当普通文本塞进一个 div，
// 字符串里的换行会被折掉——这段正好是要分行看的安全提示，挤成一坨就没人读了。
const preLine = (text:string) => () => h('div', { style: 'white-space:pre-line;line-height:1.75;' }, text)
function handleClearHostKey(s:any){
  dialog.warning({
    title: '清除已固定的主机密钥？',
    content: preLine(`面板将忘记「${s.name || s.host}」当前记住的 SSH 主机密钥，并按下一次连接看到的密钥重新记住。\n\n`
      + '✅ 机器重装过系统 / 换了机器 / 供应商做了迁移 → 这是正常操作。\n\n'
      + '⛔ 你想不出它为什么会变 → 先别清。那可能是中间人在冒充这台机器，清掉就等于把 root 凭据交给它。'),
    positiveText: '确认清除并重新连接',
    negativeText: '取消',
    onPositiveClick: async () => {
      clearing.value = s.id
      try{
        const r:any = await apiPost(`/api/admin/servers/${s.id}/clear-host-key`)
        message.success(r?.message || '已重新信任')
        if(r?.fingerprint){
          dialog.success({
            title: '已重新记住这台机器',
            content: preLine(`新指纹：${r.fingerprint}\n\n`
              + '请在这台机器上执行 ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub 核对，对得上说明连的确实是你的机器。\n\n'
              + '接着到「sing-box → 链路拓扑」点「重新下发」，把这段时间没同步上的配置和用户变更推过去。'),
            positiveText: '知道了',
          })
        }
        await load()
      }catch(e:any){ message.error(e.message) }finally{ clearing.value = null }
    },
  })
}
async function handleDelete(id:number){ try{await apiDelete(`/api/admin/servers/${id}`);message.success('已删除');await load()}catch(e:any){message.error(e.message)} }
async function moveServer(idx:number, dir:number){
  const target = idx + dir
  if(target < 0 || target >= servers.value.length || reordering.value) return
  const before = [...servers.value]
  const next = [...servers.value]
  ;[next[idx], next[target]] = [next[target], next[idx]]
  servers.value = next
  reordering.value = true
  try{
    await apiPost('/api/admin/servers/reorder', { ids: next.map(s => s.id) })
  }catch(e:any){ servers.value = before; message.error(e.message) }
  finally{ reordering.value = false }
}
async function load(){loading.value=true;try{servers.value=await apiList('/api/admin/servers')}catch{}finally{loading.value=false}}

// ---- Provider-cycle traffic analysis ----
const showTraffic = ref(false)
const trafficLoading = ref(false)
const trafficAnalysis = ref<any>(null)
const trafficChartEl = ref<HTMLElement|null>(null)
const trafficChart = shallowRef<echarts.ECharts|null>(null)
const trafficDrawerWidth = computed(() => Math.min(typeof window === 'undefined' ? 820 : window.innerWidth * .94, 820))
const trafficRemaining = computed(() => Math.max(0, (trafficAnalysis.value?.limit_bytes || 0) - (trafficAnalysis.value?.usage?.total || 0)))
const trafficPercent = computed(() => pct(trafficAnalysis.value?.usage?.total || 0, trafficAnalysis.value?.limit_bytes || 0))
const trafficIncomplete = computed(() => {
  const a = trafficAnalysis.value
  return !!a && !a.usage?.calibrated && a.usage?.sample_count > 0 && a.usage.coverage_start > a.cycle_start + 3600
})
function trafficModeLabel(mode:string){ return ({sum:'IN + OUT',max:'IN / OUT 取大',rx:'仅 IN',tx:'仅 OUT'} as Record<string,string>)[mode] || 'IN + OUT' }
function sourcePercent(total:number){ const all=trafficAnalysis.value?.attribution?.total||0; return all ? Math.max(2, Math.min(100, total/all*100)) : 0 }
async function openTrafficAnalysis(s:any){
  showTraffic.value = true; trafficLoading.value = true; trafficAnalysis.value = null
  try{
    trafficAnalysis.value = await apiGet(`/api/admin/monitor/servers/${s.id}/traffic-analysis`)
    await nextTick(); renderTrafficChart()
  }catch(e:any){ message.error(e.message) }
  finally{ trafficLoading.value = false }
}
function renderTrafficChart(){
  if(!trafficChartEl.value || !trafficAnalysis.value?.daily?.length) return
  if(!trafficChart.value) trafficChart.value = echarts.init(trafficChartEl.value)
  const rows=trafficAnalysis.value.daily
  trafficChart.value.setOption({
    animationDuration:500,
    tooltip:{trigger:'axis',valueFormatter:(v:any)=>fmtBytes(Number(v)||0)},
    legend:{top:0,data:['IN','OUT','计费量'],textStyle:{color:'#6b7785',fontSize:11}},
    grid:{left:12,right:12,top:38,bottom:8,containLabel:true},
    xAxis:{type:'category',data:rows.map((d:any)=>d.date.slice(5)),axisTick:{show:false},axisLine:{lineStyle:{color:'#dfe4ea'}},axisLabel:{color:'#7b8794'}},
    yAxis:{type:'value',axisLabel:{formatter:(v:number)=>fmtBytes(v)},splitLine:{lineStyle:{color:'rgba(80,100,120,.09)'}}},
    series:[
      {name:'IN',type:'bar',stack:'physical',data:rows.map((d:any)=>d.rx),itemStyle:{color:'#72a7c7',borderRadius:[3,3,0,0]}},
      {name:'OUT',type:'bar',stack:'physical',data:rows.map((d:any)=>d.tx),itemStyle:{color:'#7fb69b',borderRadius:[3,3,0,0]}},
      {name:'计费量',type:'line',data:rows.map((d:any)=>d.total),smooth:.28,symbolSize:6,lineStyle:{width:2,color:'#b38439'},itemStyle:{color:'#b38439'}},
    ],
  },true)
  trafficChart.value.resize()
}
function disposeTrafficChart(){ trafficChart.value?.dispose(); trafficChart.value=null; trafficAnalysis.value=null }

// ---- 节点 sing-box 版本 ----
const versions = ref<any[]>([])
const minSupported = ref('1.12.0')
const verLoading = ref(false)

async function loadVersions(){
  try{
    const d = await apiGet<any>('/api/admin/nodes/singbox')
    versions.value = d?.nodes || []
    if (d?.min_supported) minSupported.value = d.min_supported
  }catch(e:any){ message.error(e?.message || '读取节点版本失败') }
}

async function refreshVersions(){
  verLoading.value = true
  try{
    await apiPost('/api/admin/nodes/singbox/refresh')
    // 探测是后台跑的（不可达的机器要等 SSH 超时），稍等再读一次结果。
    await new Promise(r => setTimeout(r, 2500))
    await loadVersions()
  }catch(e:any){ message.error(e?.message || '检测失败') }
  finally{ verLoading.value = false }
}

function confirmUpgrade(n:any){
  dialog.warning({
    title: '确认重装 sing-box',
    content: `将在「${n.name}」上安装面板发布的 sing-box（随面板版本走）并重启服务。`
      + '安装期间这台机器上的用户会断线，客户端会自动重连。继续？',
    positiveText: '开始安装',
    negativeText: '取消',
    onPositiveClick: () => { doUpgrade(n) },
  })
}

// 安装在后端后台跑：往节点拉 ~60MB 的二进制，同步等会被服务端 WriteTimeout 掐断，
// 于是装成功了前端却报错。这里只负责发起 + 轮询节点列表里的任务状态。
async function doUpgrade(n:any){
  try{
    await apiPost(`/api/admin/nodes/${n.server_id}/singbox/upgrade`)
  }catch(e:any){ message.error(e?.message || '安装启动失败', { duration: 10000 }); return }
  message.info('已开始安装，完成后本页会自动刷新')
  await loadVersions()
  pollUpgrade(n.server_id)
}

// 轮询到该节点的任务结束为止。设上限，免得后端异常时无限轮询。
async function pollUpgrade(id:number){
  for (let i = 0; i < 120; i++){
    await new Promise(r => setTimeout(r, 5000))
    await loadVersions()
    const row = versions.value.find(v => v.server_id === id)
    if (!row || row.upgrading) continue
    if (row.upgrade_error) message.error('安装失败，详情见下方输出', { duration: 10000 })
    else message.success(`安装完成${row.version ? '：' + row.version : ''}`)
    return
  }
}

onMounted(() => { load(); loadVersions() })
onUnmounted(() => trafficChart.value?.dispose())
</script>

<style scoped>
.nv-note { font-size:12px; color:var(--text-3); line-height:1.75; margin:0 0 12px; }
.nv-list { display:flex; flex-direction:column; gap:2px; }
.nv-row {
  display:flex; flex-wrap:wrap; align-items:center; gap:8px;
  padding:8px 0; border-bottom:1px solid var(--border);
}
.nv-row:last-child { border-bottom:0; }
.nv-main { display:flex; align-items:center; gap:8px; flex:1; min-width:0; flex-wrap:wrap; }
.nv-name { font-weight:600; font-size:13px; }
.nv-host { color:var(--text-3); font-weight:400; font-size:12px; margin-left:6px; }
.nv-ver { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:13px; }
.nv-side { display:flex; align-items:center; gap:10px; }
.nv-time { font-size:11px; color:var(--text-3); }
.nv-err { flex-basis:100%; font-size:11px; line-height:1.7; color:var(--warning,#d97706); }
/* 脚本输出可能很长，限高 + 可滚动，免得一次失败把整页撑开。 */
.nv-out {
  margin:4px 0 0; padding:8px 10px; max-height:220px; overflow:auto;
  font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:11px; line-height:1.6;
  white-space:pre-wrap; word-break:break-all;
  background:var(--code-bg,rgba(128,128,128,.1)); border-radius:6px; color:var(--text-2);
}
.hint{ font-size:12px; line-height:1.6; color:var(--n-text-color-3,#8a8a8a); margin-top:6px; }
.lc-title { display:flex; align-items:center; gap:7px; }
.lc-order { display:flex; align-items:center; gap:3px; }
.order-no { display:inline-grid; place-items:center; width:20px; height:20px; border-radius:6px; background:var(--bg-soft); color:var(--text-3); font-size:10px; font-style:normal; font-variant-numeric:tabular-nums; }
.traffic-summary-grid { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:10px; }
.traffic-summary-card { min-width:0; padding:13px 14px; border:1px solid var(--border); border-radius:12px; background:var(--bg-soft); }
.traffic-summary-card span,.traffic-summary-card small { display:block; color:var(--text-3); font-size:10px; }
.traffic-summary-card b { display:block; margin:4px 0 2px; color:var(--text); font-size:18px; font-variant-numeric:tabular-nums; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.traffic-summary-card.capacity { background:linear-gradient(145deg,rgba(91,145,112,.12),rgba(91,145,112,.04)); }
.quota-overview { margin-top:12px; padding:13px 14px; border:1px solid var(--border); border-radius:12px; }
.quota-overview>div { display:flex; justify-content:space-between; margin-bottom:7px; font-size:12px; }
.quota-overview p { margin:7px 0 0; color:var(--text-3); font-size:11px; line-height:1.6; }
.traffic-section { margin-top:18px; padding-top:16px; border-top:1px solid var(--border); }
.traffic-section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:10px; }
.traffic-section h3 { margin:0; font-size:14px; }
.traffic-section-head p { margin:3px 0 0; color:var(--text-3); font-size:11px; }
.traffic-chart { width:100%; height:270px; margin-top:10px; }
.coverage-chip { padding:3px 7px; border-radius:6px; background:var(--bg-soft); color:var(--text-3); font-size:10px; white-space:nowrap; }
.source-overview { display:flex; flex-wrap:wrap; gap:8px 18px; margin:12px 0; padding:10px 12px; border-radius:10px; background:var(--bg-soft); color:var(--text-3); font-size:11px; }
.source-overview b { color:var(--text); }
.traffic-sources { display:flex; flex-direction:column; gap:10px; }
.traffic-source-row { display:flex; align-items:flex-start; gap:9px; }
.source-rank { display:grid; place-items:center; width:22px; height:22px; border-radius:7px; background:var(--bg-soft); color:var(--text-3); font-size:10px; }
.source-main { flex:1; min-width:0; }
.source-main>div:first-child { display:flex; justify-content:space-between; gap:12px; font-size:12px; }
.source-main>div:first-child span { color:var(--text-2); font-variant-numeric:tabular-nums; }
.source-track { height:5px; margin:5px 0; border-radius:4px; overflow:hidden; background:var(--bg); }
.source-track i { display:block; height:100%; border-radius:inherit; background:linear-gradient(90deg,#6e9fbe,#75aa8f); }
.source-main small { color:var(--text-3); font-size:10px; }
@media(max-width:700px){ .traffic-summary-grid{grid-template-columns:repeat(2,minmax(0,1fr));} .traffic-chart{height:230px;} }
</style>

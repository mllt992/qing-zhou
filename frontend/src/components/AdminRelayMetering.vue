<template>
  <section class="relay-metering" aria-label="中转链路计量">
    <details @toggle="togglePanel">
      <summary>中转链路计量 <span>{{ loaded ? (state.enabled ? '设置已开启' : '设置未开启') : '状态待确认' }}</span></summary>
      <p>保存的是计量设置，节点下发成功后才会生效。系统先让落地接受新身份，再切换入口；每台机器分别记录实测流量，用户套餐仍只在入口扣一次</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <form @submit.prevent="requestConfirmation">
        <label><input v-model="enabled" type="checkbox" :disabled="formDisabled">启用每条入口到落地的独立中转身份</label>
        <label><input v-model="cumulative" type="checkbox" :disabled="formDisabled || state.cumulative_started">启用累计快照采集，避免每次读取清零</label>
        <small>累计模式需要确认 sing-box 的进程代次；不支持的节点保留旧式采集。已切换的节点不能直接退回清零模式，避免重复扣费</small>
        <label><input v-model="perUser" type="checkbox" :disabled="formDisabled || !enabled">启用逐用户中转实测（需先启用独立中转身份）</label>
        <small>用户现有账号、密码和订阅不变。支持 VLESS、VMess、Trojan、TUIC、Hysteria/Hysteria2、AnyTLS 和 SS2022 AES-128/256；mixed 仅作入口。协议和传输的实测范围以该版本验收记录为准。内部身份会增加配置规模；旧共享流量和历史记录仍单列，未归属流量不会均摊给用户</small>
        <div class="actions">
          <button type="submit" :disabled="formDisabled">{{ preflighting ? '预检中…' : '检查变更影响' }}</button>
          <button type="button" :disabled="busy || loading || preflighting" @click="refresh">刷新状态</button>
        </div>
      </form>

      <div v-if="confirming" class="confirmation" aria-label="变更预检">
        <h4>1. 预检变更</h4>
        <p v-if="preflighting" role="status">正在读取拓扑和已有内核检测记录，还没有保存或下发</p>
        <template v-if="preflight">
          <p role="status">{{ preflight.valid ? '设置预检通过，请核对下面的节点和影响后确认' : '预检未通过，设置尚未保存' }}</p>
          <ul v-if="preflight.errors.length" class="error" role="alert"><li v-for="(reason, index) in preflight.errors" :key="index">{{ reason }}</li></ul>
          <p>{{ preflight.scope_note }}</p>
          <h4>受影响节点（{{ preflight.nodes.length }} 台）</h4>
          <ul class="node-list">
            <li v-for="node in preflight.nodes" :key="node.server_id">
              <div class="node-heading"><b>{{ node.name }}</b><span>{{ node.requires_reinstall ? '需要重装内核' : node.requires_check ? '需要重新检测' : '已有能力记录' }}</span></div>
              <small>已安装内核 {{ node.version || '未知' }} · 用户流量统计：{{ capability(node, node.has_v2ray_api) }}<template v-if="node.vision_required"> · Vision 修复标记：{{ capability(node, node.has_vision_framing_fix) }}</template><template v-if="node.transport_required"> · WebSocket/HTTPUpgrade 修复标记：{{ capability(node, !!node.has_transport_read_buffer_fix) }}</template><template v-if="node.trojan_required"> · Trojan 分段握手修复标记：{{ capability(node, !!node.has_trojan_handshake_fix) }}</template></small>
              <small v-if="node.checked_at">上次检测：{{ formatTime(node.checked_at) }}（运行能力仍以下发时核验为准）</small>
              <ul v-if="node.reasons.length"><li v-for="(reason,index) in node.reasons" :key="index">{{ reason }}</li></ul>
            </li>
          </ul>
          <p v-if="preflight.nodes.some(n => n.requires_reinstall || n.requires_check)">请到“服务器”的内核管理查看、重装或重新检测，再回来预检。此处不会自动安装内核</p>
          <p>启用链路计量会生成并加密保存内部中转凭据，仅下发到相关节点。配置变化会重启 sing-box，可能中断现有连接。旧共享凭据继续兼容，不会立即撤销</p>
          <p v-if="perUser">逐用户中转将按用户和线路生成内部身份，并先准备落地再切换入口。所有机器分别实测，不复制入口用量；套餐不在落地重复扣除</p>
          <p v-else-if="state.per_user_enabled">关闭逐用户中转后，新下发路径将退回共享链路计量；已有逐用户历史记录保留</p>
          <p>累计采集会执行一次旧计数清零交接，随后持续读取累计值；进程崩溃前未采到的尾部仍可能缺失</p>
        </template>
        <div class="actions">
          <button :disabled="busy || preflighting || !preflight?.valid" @click="save">{{ busy ? '正在保存…' : '确认保存并下发' }}</button>
          <button :disabled="busy" @click="cancelConfirmation">取消</button>
        </div>
      </div>

      <div v-if="saved || nodeRows.length || state.sync?.['-1']" class="rollout" aria-label="节点下发进度">
        <h4>{{ saved ? '2. 保存与下发' : '最近下发状态' }}</h4>
        <p role="status">{{ rolloutMessage }}</p>
        <p v-if="polling" class="muted">正在自动刷新实际结果；关闭此面板或离开页面会停止刷新，不会撤销已保存的设置</p>
        <button v-if="pollPaused && !busy && !loading" type="button" @click="refresh">继续检查结果</button>
        <p v-if="aggregateFailure" class="error">整体下发：{{ aggregateFailure }}</p>
        <ul class="node-list">
          <li v-for="node in nodeRows" :key="node.server_id">
            <div class="node-heading"><b>{{ node.name }}</b><span :class="{error: node.outcome === 'failed'}">{{ syncLabel(node.outcome) }}</span></div>
            <small v-if="node.error" class="error">{{ node.error }}</small>
            <small v-if="node.at">最近结果：{{ formatTime(node.at) }}</small>
          </li>
        </ul>
      </div>
      <h4 v-if="state.links.length">链路生效确认</h4>
      <ul v-if="state.links.length" class="links">
        <li v-for="link in state.links" :key="link.id"><span>{{ link.source_name }} → {{ link.target_name }}</span><b>{{ phaseLabel(link.state) }}</b></li>
      </ul>
      <p v-else-if="loaded && !loading && !error">尚无独立链路身份；已采集的旧共享中转仍会在机器来源中单独显示</p>
      <div v-if="state.credentials?.length" class="credentials">
        <h4>兼容凭据生命周期</h4><p>系统核验受管入口、采集静默窗口和待归属流量；手工配置的中转也必须由管理员确认完成迁移。停用后需先恢复并确认旧共享凭据，才能关闭链路计量</p>
        <div v-for="item in state.credentials" :key="credentialKey(item)" class="credential-row">
          <div><b>{{ item.name }}</b> · {{ credentialPhase(item.state) }}<small v-if="item.reason">{{ item.reason }}</small></div>
          <button v-if="item.state==='active'" :disabled="busy || confirming || !item.can_retire" @click="credentialAction={credential:item,action:'retire'}">停用旧凭据</button>
          <button v-if="item.state==='retired'||item.state==='retiring'" :disabled="busy || confirming" @click="credentialAction={credential:item,action:'restore'}">恢复兼容</button>
        </div>
      </div>
      <div v-if="credentialAction" class="confirmation" role="alert">
        <p>目标：{{ credentialAction.credential.name }}（服务器 #{{ credentialAction.credential.server_id }}，入站 #{{ credentialAction.credential.inbound_id }}<template v-if="credentialAction.credential.user_id">，用户 #{{ credentialAction.credential.user_id }}</template>）</p>
        <p v-if="credentialAction.action==='retire'">确认所有手工中转也已迁移？停用将使仍使用该旧凭据的连接失效；配置变更可能中断当前连接</p>
        <p v-else>恢复会重新允许该旧凭据在目标入站认证；配置变更可能中断当前连接</p>
        <div class="actions"><button :disabled="busy" @click="changeCredential">确认凭据变更</button><button :disabled="busy" @click="credentialAction=null">取消</button></div>
      </div>
    </details>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { apiGet, apiPut, apiPost } from '@/api'

interface Credential {kind:string;link_id:number;server_id:number;inbound_id:number;user_id?:number;generation:number;state:string;name:string;can_retire:boolean;reason:string}
interface Link {id:number;source_name:string;target_name:string;source_server_id?:number;target_server_id?:number;state:string}
interface Sync {state:string;error?:string;at?:number;revision?:number;request_revision?:number;started_revision?:number}
interface Ticket {epoch:string;revision:number}
interface Node {server_id:number;name:string;version:string;has_v2ray_api:boolean;has_vision_framing_fix:boolean;vision_required:boolean;has_transport_read_buffer_fix?:boolean;transport_required?:boolean;has_trojan_handshake_fix?:boolean;trojan_required?:boolean;checked_at:number;error?:string;requires_reinstall:boolean;requires_check:boolean;reasons:string[]}
interface State {enabled:boolean;per_user_enabled:boolean;cumulative_enabled:boolean;cumulative_started:boolean;links:Link[];credentials?:Credential[];sync?:Record<string,Sync>;sync_epoch?:string;nodes?:Node[]}
interface Preflight {valid:boolean;errors:string[];nodes:Node[];scope_note:string}
const emptyState = (): State => ({enabled:false,per_user_enabled:false,cumulative_enabled:false,cumulative_started:false,links:[]})
const state=ref<State>(emptyState())
const enabled=ref(false), cumulative=ref(false), perUser=ref(false)
const loaded=ref(false), busy=ref(false), loading=ref(false), confirming=ref(false), saved=ref(false), error=ref('')
const preflighting=ref(false), preflight=ref<Preflight|null>(null), credentialAction=ref<{credential:Credential;action:'retire'|'restore'}|null>(null)
const ticket=ref<Ticket|null>(null), polling=ref(false), pollPaused=ref(false), panelOpen=ref(false)
const formDisabled=computed(()=>busy.value || loading.value || !loaded.value || confirming.value || !!credentialAction.value)
const aggregate=computed(()=>state.value.sync?.['-1'])
const epochChanged=computed(()=>!!ticket.value && !!state.value.sync_epoch && state.value.sync_epoch!==ticket.value.epoch)
const trackedAggregate=computed(()=>ticket.value && state.value.sync_epoch===ticket.value.epoch && (aggregate.value?.request_revision||0)>=ticket.value.revision ? aggregate.value : undefined)
const aggregateFailure=computed(()=>ticket.value ? (trackedAggregate.value?.state==='failed' ? trackedAggregate.value.error||'未提供具体错误' : '') : (aggregate.value?.state==='failed' ? aggregate.value.error||'未提供具体错误' : ''))
const nodeRows=computed(()=>{
  const names=new Map<number,string>()
  for(const node of state.value.nodes||preflight.value?.nodes||[]) names.set(node.server_id,node.name)
  for(const link of state.value.links){
    if(link.source_server_id!==undefined && !names.has(link.source_server_id)) names.set(link.source_server_id,link.source_name)
    if(link.target_server_id!==undefined && !names.has(link.target_server_id)) names.set(link.target_server_id,link.target_name)
  }
  for(const id of Object.keys(state.value.sync||{})) if(Number(id)>=0 && !names.has(Number(id))) names.set(Number(id),Number(id)===0?'面板本机':`服务器 #${id}`)
  return [...names].sort(([a],[b])=>a-b).map(([server_id,name])=>{
    const status=state.value.sync?.[String(server_id)]
    const fresh=!ticket.value || (!!trackedAggregate.value?.started_revision && (status?.revision||0)>trackedAggregate.value.started_revision)
    return {server_id,name,outcome:fresh ? status?.state||'waiting' : 'waiting',error:fresh?status?.error:'',at:status?.at}
  })
})
const rolloutMessage=computed(()=>{
  if(epochChanged.value) return '面板已重启，无法继续确认刚才那次下发；请刷新查看当前节点和链路状态'
  if(saved.value && !ticket.value) return '设置已保存，但未收到可追踪的下发凭据；以下仅为最近记录，请刷新核对'
  if(ticket.value){
    if(trackedAggregate.value?.state==='failed') return '设置已保存，但下发有失败；请逐项查看节点错误，尚未确认的节点仍显示等待'
    if(trackedAggregate.value?.state==='ok') return nodeRows.value.length && nodeRows.value.every(n=>n.outcome==='ok') ? '本轮节点下发已成功；链路是否已切换请看下方生效确认' : '本轮下发已结束，仍有节点缺少本次成功确认，请查看节点结果'
    if(pollPaused.value) return '设置已保存，自动刷新已暂停；仍在等待下发结果，可继续检查'
    return '设置已保存，正在等待节点下发；这还不代表计量已生效'
  }
  return '以下是服务器最近一次返回的下发记录；设置已开启不代表所有链路已经切换'
})
let alive=true, viewVersion=0, readVersion=0, checkVersion=0, polls=0
let readController:AbortController|undefined, checkController:AbortController|undefined, timer:ReturnType<typeof setTimeout>|undefined
const POLL_LIMIT=60, POLL_DELAY=2000
function stopPolling(paused=false){clearTimeout(timer);timer=undefined;polling.value=false;pollPaused.value=paused}
function cancelReads(){readVersion++;readController?.abort();readController=undefined;loading.value=false}
function cancelConfirmation(){checkVersion++;checkController?.abort();checkController=undefined;confirming.value=false;preflighting.value=false;preflight.value=null}
function togglePanel(event:Event){
  const open=(event.target as HTMLDetailsElement).open
  if(panelOpen.value===open)return
  panelOpen.value=open
  if(!open){viewVersion++;cancelReads();cancelConfirmation();credentialAction.value=null;stopPolling(!!ticket.value)}
  else if(!busy.value) void refresh()
}
function currentDraft(){return {enabled:enabled.value,cumulative_enabled:cumulative.value,per_user_enabled:perUser.value}}
function applyState(response:State,resetDraft:boolean){
  state.value={...emptyState(),...response,links:response.links||[],credentials:response.credentials||[]}
  loaded.value=true
  if(resetDraft){enabled.value=!!response.enabled;cumulative.value=!!response.cumulative_enabled||!!response.cumulative_started;perUser.value=!!response.per_user_enabled}
}
async function load(resetDraft=false):Promise<boolean>{
  cancelReads()
  const version=readVersion, view=viewVersion, controller=new AbortController()
  readController=controller;loading.value=true;error.value=''
  try{
    const response=await apiGet<State>('/api/admin/relay-metering',{signal:controller.signal})
    if(!alive || version!==readVersion || view!==viewVersion)return false
    applyState(response,resetDraft);return true
  }catch(e:unknown){
    if(alive && version===readVersion && view===viewVersion && !controller.signal.aborted){error.value=messageOf(e,'读取链路计量失败');stopPolling(!!ticket.value)}
    return false
  }finally{if(alive && version===readVersion){loading.value=false;readController=undefined}}
}
function shouldPoll(){return !!ticket.value && !epochChanged.value && !['ok','failed'].includes(trackedAggregate.value?.state||'')}
function schedulePoll(){
  if(!alive || !panelOpen.value || !shouldPoll()){stopPolling();return}
  if(polls>=POLL_LIMIT){stopPolling(true);return}
  polling.value=true
  timer=setTimeout(async()=>{timer=undefined;polls++;if(await load())schedulePoll()},POLL_DELAY)
}
async function refresh(){
  if(busy.value)return
  stopPolling();cancelConfirmation();polls=0
  if(await load(true))schedulePoll()
}
async function requestConfirmation(){
  if(formDisabled.value)return
  stopPolling();preflight.value=null;confirming.value=true;preflighting.value=true;error.value=''
  const version=++checkVersion, view=viewVersion, controller=new AbortController()
  checkController=controller
  const query=new URLSearchParams(Object.entries(currentDraft()).map(([key,value])=>[key,String(value)]))
  try{
    const response=await apiGet<Preflight>(`/api/admin/relay-metering/preflight?${query}`,{signal:controller.signal})
    if(!alive || version!==checkVersion || view!==viewVersion)return
    if(!response || typeof response.valid!=='boolean' || !Array.isArray(response.nodes) || !Array.isArray(response.errors))throw new Error('预检结果不完整，请刷新后重试')
    preflight.value=response
  }catch(e:unknown){if(alive && version===checkVersion && !controller.signal.aborted)error.value=messageOf(e,'预检失败，设置尚未保存')}
  finally{if(alive && version===checkVersion){preflighting.value=false;checkController=undefined}}
}
watch(enabled,value=>{if(!value)perUser.value=false})
watch([enabled,cumulative,perUser],()=>{if(confirming.value)cancelConfirmation()})
function acceptTicket(response:{sync_ticket?:Ticket}){
  const value=response.sync_ticket
  ticket.value=value && typeof value.epoch==='string' && value.epoch && Number.isSafeInteger(value.revision) && value.revision>0 ? value : null
  saved.value=true;polls=0;pollPaused.value=false
}
async function save(){
  if(busy.value || !loaded.value || !confirming.value || preflighting.value || !preflight.value?.valid)return
  const view=viewVersion;busy.value=true;error.value='';stopPolling();saved.value=false;ticket.value=null
  try{
    const response=await apiPut<{sync_ticket?:Ticket}>('/api/admin/relay-metering',{...currentDraft(),confirm:true})
    if(!alive || view!==viewVersion)return
    confirming.value=false;acceptTicket(response)
    if(await load())schedulePoll()
  }catch(e:unknown){if(alive && view===viewVersion)error.value=`${messageOf(e,'保存请求失败')}；尚未确认是否保存，请刷新核对后再试`}
  finally{busy.value=false;if(alive && view!==viewVersion && panelOpen.value)void refresh()}
}
async function changeCredential(){
  if(!credentialAction.value || busy.value)return
  const view=viewVersion, action=credentialAction.value
  busy.value=true;error.value='';stopPolling();saved.value=false;ticket.value=null
  try{
    const response=await apiPost<{sync_ticket?:Ticket}>('/api/admin/relay-metering/credentials',{...action.credential,action:action.action,confirm:true})
    if(!alive || view!==viewVersion)return
    credentialAction.value=null;acceptTicket(response)
    if(await load())schedulePoll()
  }catch(e:unknown){if(alive && view===viewVersion)error.value=messageOf(e,'凭据变更未确认，请刷新核对')}
  finally{busy.value=false;if(alive && view!==viewVersion && panelOpen.value)void refresh()}
}
function messageOf(e:unknown,fallback:string){return e instanceof Error && e.message ? e.message : fallback}
function phaseLabel(phase:string){return ({prepared:'等待落地接受',accepted:'落地已接受，等待入口切换',active:'入口已切换'} as Record<string,string>)[phase]||phase}
function syncLabel(phase:string){return ({pending:'等待下发',running:'下发中',ok:'下发成功',failed:'下发失败',waiting:ticket.value?'等待本次确认':'尚无确认结果'} as Record<string,string>)[phase]||'状态待确认'}
function credentialPhase(phase:string){return ({current:'当前代',active:'旧凭据兼容中',retiring:'等待停用下发',retired:'已确认停用',restoring:'等待恢复确认'} as Record<string,string>)[phase]||phase}
function credentialKey(item:Credential){return `${item.kind}:${item.link_id}:${item.inbound_id}:${item.user_id||0}:${item.generation}`}
function capability(node:Node,supported:boolean){return !node.checked_at || !node.version || node.error ? '待确认' : supported?'已检测到支持':'未检测到支持'}
function formatTime(at:number){return new Date(at*1000).toLocaleString()}
onMounted(()=>void load(true))
onUnmounted(()=>{alive=false;viewVersion++;cancelReads();cancelConfirmation();stopPolling()})
</script>
<style scoped>
.relay-metering{border:1px solid var(--border);border-radius:12px;padding:14px 16px;margin-bottom:16px;background:var(--card);font-size:12px}.relay-metering summary{cursor:pointer;font-size:14px;font-weight:600}.relay-metering summary span{margin-left:12px;color:var(--text-3);font-size:12px}.relay-metering p,.relay-metering small{line-height:1.8;color:var(--text-3)}.relay-metering label{display:flex;gap:8px;align-items:center;margin:12px 0;color:var(--text)}.relay-metering h4{margin:12px 0 6px}.actions{display:flex;flex-wrap:wrap;gap:10px;margin-top:12px}.relay-metering button{border:1px solid var(--border);background:var(--bg-soft);color:var(--text);border-radius:6px;padding:6px 12px;cursor:pointer}.relay-metering button:disabled{opacity:.5;cursor:default}.confirmation,.rollout{padding:10px 14px;margin-top:12px;border:1px solid var(--border);border-radius:8px}.confirmation{border-color:var(--warning,#b6813e)}.links,.node-list{padding:0;list-style:none}.links>li,.node-list>li{padding:10px 0;border-bottom:1px solid var(--border)}.links>li,.node-heading{display:flex;justify-content:space-between;gap:12px}.node-list small{display:block;overflow-wrap:anywhere}.node-list ul{padding-left:20px;line-height:1.8}.node-heading span,.links b{font-size:11px;flex-shrink:0}.relay-metering .error{color:var(--danger,#b45151);white-space:pre-wrap;overflow-wrap:anywhere}.credential-row{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:10px 0;border-bottom:1px solid var(--border)}.credential-row small{display:block}.credential-row button{flex-shrink:0}@media(max-width:520px){.links>li,.credential-row,.node-heading{flex-wrap:wrap}.confirmation,.rollout{padding:8px 10px}}
</style>

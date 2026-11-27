import { beforeEach, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import AdminServers from '@/views/AdminServers.vue'
import { apiGet, apiList, apiPost } from '@/api'
vi.mock('@/api',()=>({apiGet:vi.fn(),apiList:vi.fn(),apiPost:vi.fn(),apiPut:vi.fn(),apiDelete:vi.fn()}))
vi.mock('naive-ui',async importOriginal=>({...await importOriginal<typeof import('naive-ui')>(),useMessage:()=>({error:vi.fn(),success:vi.fn(),info:vi.fn()}),useDialog:()=>({warning:vi.fn()})}))
vi.mock('echarts',()=>({init:vi.fn()}))
const vision='1.14.2+qz-vmess.9b95ab8c9478'
const transport=vision+'-transport.07512b10'
const current='1.14.5+qz-trojan.b85be9da'
const srv=(id:number,name:string)=>({id,name,host:'10.0.0.'+id,port:22,enabled:true})
beforeEach(()=>{vi.resetAllMocks();vi.mocked(apiList).mockResolvedValue([srv(1,'Vision only'),srv(2,'Full transport fix')])})
it('displays Vision and transport installed markers separately and never upgrades automatically',async()=>{
 vi.mocked(apiGet).mockResolvedValue({vision_fixed_version:vision,transport_fixed_version:transport,current_release_version:current,trojan_fixed_version:current,nodes:[{server_id:1,name:'Vision only',version:vision,has_v2ray_api:true,has_vision_framing_fix:true,has_transport_read_buffer_fix:false},{server_id:2,name:'Full transport fix',version:transport,has_v2ray_api:true,has_vision_framing_fix:true,has_transport_read_buffer_fix:true}]})
 const w=shallowMount(AdminServers,{global:{renderStubDefaultSlot:true}});await flushPromises()
 const rows=w.findAll('.srv-card');expect(rows).toHaveLength(2)
 expect(rows[0]!.text()).toContain('版本含 Vision 修复标记');expect(rows[0]!.text()).toContain('未确认 WS/HTTPUpgrade 修复')
 expect(rows[1]!.text()).toContain('版本含 WS/HTTPUpgrade 修复标记')
 expect(w.text()).toContain('更换磁盘文件不代表运行中的进程已更新');expect(w.text()).toContain('不能仅凭官方版本号较新判断支持');expect(w.text()).toContain(transport);expect(w.text()).toContain(current)
 expect(apiPost).not.toHaveBeenCalled();w.unmount()
})
it('retains probe errors beside previously observed repair markers',async()=>{
 vi.mocked(apiGet).mockResolvedValue({transport_fixed_version:transport,nodes:[{server_id:1,name:'stale observation',version:transport,has_vision_framing_fix:true,has_transport_read_buffer_fix:true,error:'runtime unavailable'}]})
 const w=shallowMount(AdminServers,{global:{renderStubDefaultSlot:true}});await flushPromises();expect(w.text()).toContain('探测失败：runtime unavailable');expect(w.text()).not.toContain('运行已就绪');w.unmount()
})

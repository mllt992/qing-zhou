import { expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import AdminServiceTraffic from '@/components/AdminServiceTraffic.vue'
const service = {total:1500,billable_total:500,new_coverage_start:1,user_coverage_complete:false,sources:[{kind:'relay_link',link_id:1,user_id:0,name:'DMIT → Verizon',up:400,down:600,total:1000},{kind:'unknown',link_id:0,user_id:0,name:'未知代理身份',up:200,down:300,total:500}],quality:{mode:'reset',status:'ok',gaps:0,pending_polls:0}}
it('separates machine service, billing and relay observations without claiming exact NIC reconciliation',()=>{
 const w=mount(AdminServiceTraffic,{props:{service}})
 expect(w.text()).toContain('本机代理业务来源');expect(w.text()).toContain('DMIT → Verizon');expect(w.text()).toContain('仅观测，不重复扣费');expect(w.text()).toContain('暂停按人数估算容量');expect(w.text()).toContain('入口扣一次');w.unmount()
})
it('shows pending and missing reads instead of presenting them as zero',()=>{
 const w=mount(AdminServiceTraffic,{props:{service:{...service,sources:[],quality:{mode:'reset',status:'unavailable',gaps:2,pending_polls:3}}}})
 expect(w.text()).toContain('采集失败');expect(w.text()).toContain('3 个采集批次');expect(w.text()).toContain('2 个统计边界');expect(w.text()).toContain('不能据此判断没有使用');w.unmount()
})
it('paginates complete source data without changing the overall total',async()=>{
 const sources=Array.from({length:12},(_,i)=>({...service.sources[0]!,link_id:i+1,name:`link ${i+1}`}))
 const w=mount(AdminServiceTraffic,{props:{service:{...service,sources}}});expect(w.findAll('li')).toHaveLength(10)
 await w.findAll('button').find(b=>b.text()==='下一页')!.trigger('click');expect(w.findAll('li')).toHaveLength(2);expect(w.text()).toContain('link 12');expect(w.text()).toContain('1.46 KB')
 await w.setProps({service});expect(w.findAll('li')).toHaveLength(2);w.unmount()
})

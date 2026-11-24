<template>
  <div>
    <div class="page-head">
      <div><h2 class="page-title">公告管理</h2><p class="page-sub">管理公告内容、展示时段、置顶与启用状态</p></div>
      <div class="page-actions"><n-button type="primary" @click="openCreate">发布公告</n-button></div>
    </div>
    <div class="resource-overview">
      <div class="resource-metric"><b>{{ announcements.length }}</b><span>全部公告</span></div>
      <div class="resource-metric success"><b>{{ announcements.filter(a => a.enabled).length }}</b><span>当前启用</span></div>
      <div class="resource-metric"><b>{{ announcements.filter(a => a.pinned).length }}</b><span>置顶公告</span></div>
    </div>
    <n-spin :show="loading">
      <div v-if="announcements.length" class="card-grid">
        <div v-for="a in announcements" :key="a.id" class="list-card">
          <div class="lc-head">
            <span class="lc-title">{{ a.title || '—' }}</span>
            <n-space :size="4">
              <n-tag v-if="a.pinned" type="warning" size="tiny" :bordered="false">置顶</n-tag>
              <n-tag :type="a.enabled ? 'success' : 'default'" size="tiny" :bordered="false">{{ a.enabled ? '启用' : '禁用' }}</n-tag>
            </n-space>
          </div>
          <div v-if="a.content" class="lc-meta" style="color:var(--text-2);display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;">{{ a.content }}</div>
          <div class="lc-meta">
            <span class="kv">{{ fmtDate(a.created_at) }}</span>
          </div>
          <div class="lc-foot">
            <n-button size="tiny" @click="openEdit(a)">编辑</n-button>
            <span style="flex:1;" />
            <n-button size="tiny" type="error" quaternary @click="handleDelete(a.id)">删除</n-button>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="暂无公告" style="padding:40px 0;" />
    </n-spin>

    <n-modal v-model:show="showForm" preset="card" :title="editing?'编辑公告':'发布公告'" style="max-width:600px;">
      <n-form label-placement="left" label-width="80">
        <n-form-item label="标题"><n-input v-model:value="form.title" /></n-form-item>
        <n-form-item label="内容"><n-input v-model:value="form.content" type="textarea" :rows="6" /></n-form-item>
        <n-form-item label="开始时间"><n-input v-model:value="form.start_at" :input-props="{ type: 'datetime-local' }" /></n-form-item>
        <n-form-item label="结束时间"><n-input v-model:value="form.end_at" :input-props="{ type: 'datetime-local' }" /></n-form-item>
        <n-form-item label="置顶"><n-switch v-model:value="form.pinned" /></n-form-item>
        <n-form-item label="启用"><n-switch v-model:value="form.enabled" /></n-form-item>
      </n-form>
      <n-button type="primary" block :loading="saving" @click="handleSave">保存</n-button>
    </n-modal>
  </div>
</template>
<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { NSpin, NButton, NModal, NForm, NFormItem, NInput, NSwitch, NTag, NSpace, NEmpty, useMessage } from 'naive-ui'
import { apiList, apiPost, apiPut, apiDelete } from '@/api'
import { fmtDate, toLocalDatetimeInput } from '@/utils/format'
const message = useMessage()
const announcements = ref<any[]>([])
const loading = ref(false); const saving = ref(false)
const showForm = ref(false); const editing = ref<any>(null)
const form = reactive({ title:'', content:'', pinned:false, enabled:true, start_at:'', end_at:'' })
function toLocal(ts:number){ return toLocalDatetimeInput(ts) }
function toUnix(s:string){ return s ? Math.floor(new Date(s).getTime()/1000) : 0 }
function openCreate(){ editing.value=null; Object.assign(form,{title:'',content:'',pinned:false,enabled:true,start_at:'',end_at:''}); showForm.value=true }
function openEdit(a:any){ editing.value=a; Object.assign(form,{title:a.title,content:a.content,pinned:a.pinned,enabled:a.enabled,start_at:toLocal(a.start_at),end_at:toLocal(a.end_at)}); showForm.value=true }
async function handleSave(){
  saving.value=true
  try{ const body={...form,start_at:toUnix(form.start_at),end_at:toUnix(form.end_at)}
    if(editing.value) await apiPut(`/api/admin/announcements/${editing.value.id}`,body)
    else await apiPost('/api/admin/announcements',body)
    message.success('保存成功'); showForm.value=false; await load()
  }catch(e:any){message.error(e.message)}finally{saving.value=false}
}
async function handleDelete(id:number){ try{await apiDelete(`/api/admin/announcements/${id}`);message.success('已删除');await load()}catch(e:any){message.error(e.message)} }
async function load(){loading.value=true;try{announcements.value=await apiList('/api/admin/announcements')}catch{}finally{loading.value=false}}
onMounted(load)
</script>

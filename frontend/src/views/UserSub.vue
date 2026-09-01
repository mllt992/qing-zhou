<template>
  <div>
    <!-- 页面头 -->
    <div class="sub-head">
      <div>
        <h2 class="page-title">订阅管理</h2>
        <p class="page-sub">订阅链接、导入格式与节点开关，都在这里管理</p>
      </div>
      <n-space size="small">
        <n-button size="small" secondary @click="router.push('/dashboard')">
          <template #icon><n-icon><SpeedometerOutline /></n-icon></template>
          控制台
        </n-button>
        <n-button size="small" secondary @click="router.push('/orders')">订单记录</n-button>
        <n-button size="small" type="primary" @click="router.push('/shop')">去商城</n-button>
      </n-space>
    </div>

    <div class="sub-summary" aria-label="订阅状态摘要">
      <div class="sub-stat"><span>生效套餐</span><b>{{ activePlanCount }}</b><small>排队 {{ queuedPlanCount }} 份</small></div>
      <div class="sub-stat"><span>可用节点</span><b>{{ enabledNodeCount }} / {{ nodes.length }}</b><small>禁用 {{ disabledNodeCount }} 个</small></div>
      <div class="sub-stat"><span>代理入口</span><b>{{ proxies.length }}</b><small>HTTP / SOCKS5 / HTTPS</small></div>
      <div class="sub-stat"><span>订阅状态</span><b>{{ sub.url ? '已就绪' : '未生成' }}</b><small>{{ sub.url ? '支持 4 种导入格式' : '购买或分配套餐后生成' }}</small></div>
    </div>

    <!-- 订阅链接 -->
    <n-card size="small" class="sec sub-link-card">
      <template #header>
        <span class="sec-title">订阅链接</span>
        <span class="sec-caption">复制后导入客户端，地址包含访问凭据，请勿公开分享</span>
      </template>
      <div class="routing-choice">
        <div class="routing-choice-label">原生配置代理范围</div>
        <n-select v-model:value="routingProfile" :options="routingProfileOptions" size="small" class="routing-choice-select" />
        <div class="routing-choice-note">{{ routingProfileNote }}</div>
      </div>
      <n-input-group>
        <n-input :value="selectedSubscriptionURL" readonly placeholder="暂无订阅" />
        <n-button type="primary" @click="copy(selectedSubscriptionURL)">复制</n-button>
      </n-input-group>
      <div class="routing-compat-note">作用于下方 Clash / sing-box / Surge 配置；只影响这次复制并重新导入的链接，已在使用的订阅不会改变。</div>
      <div class="sub-action-row">
        <span class="sub-action-label">分流配置</span>
        <n-button size="small" secondary @click="copy(selectedFormats?.clash)">Clash</n-button>
        <n-button size="small" secondary @click="copy(selectedFormats?.singbox)">sing-box</n-button>
        <n-button size="small" secondary @click="copy(selectedFormats?.surge)">Surge</n-button>
      </div>
      <div class="sub-action-row">
        <span class="sub-action-label">仅节点</span>
        <!-- formats.base64, not formats.default: default has no ?format= and so
             picks its output from the client's User-Agent, which silently hands
             YAML to anything whose UA contains "clash". This button is for
             v2rayN / NekoBox / Shadowrocket, so it must pin the link list. -->
        <n-tooltip trigger="hover">
          <template #trigger>
            <n-button size="small" secondary @click="copy(base64SubscriptionURL)">通用 / v2rayN</n-button>
          </template>
          通用格式只包含节点，代理范围由客户端本地规则决定
        </n-tooltip>
        <n-button size="small" @click="showQr=!showQr">{{ showQr?'隐藏':'显示' }}二维码</n-button>
      </div>
      <div class="sub-action-row safety">
        <span class="sub-action-label">安全操作</span>
        <!-- 两个按钮代价完全不同，分开呈现：换地址是纯面板操作、立即生效、不影响
             任何人；换凭据要同步到每个节点才生效，因此默认禁用 + 30 天冷却。 -->
        <n-button size="small" type="warning" @click="handleResetSub">更换订阅地址</n-button>
        <!-- 禁用与否跟随后端开关，不写死：后端本来就要校验 node_creds_reset_enabled，
             按钮读同一个值才不会出现「管理员开了但按钮还是灰的」。 -->
        <n-tooltip trigger="hover" :disabled="credsResetEnabled">
          <template #trigger>
            <n-button size="small" type="error" :disabled="!credsResetEnabled" :loading="resettingCreds"
                      @click="handleResetNodeCreds">重置节点凭据</n-button>
          </template>
          该功能暂时禁用，有需要请联系管理员
        </n-tooltip>
      </div>
      <div class="sub-security-note">
        订阅地址泄露时用「更换订阅地址」：旧地址立即失效，无需重启节点。
        注意它不会使已经导出的节点失效——那需要「重置节点凭据」。
      </div>
      <div v-if="showQr && selectedSubscriptionURL" style="margin-top:12px;text-align:center;">
        <canvas ref="qrCanvas" />
        <div style="font-size:11px;color:var(--text-3);margin-top:4px;">手机扫描导入订阅</div>
      </div>
    </n-card>

    <!-- 我的套餐：每个套餐独立计量，各自展示剩余流量与到期时间（可能多份并存，不合并） -->
    <n-card v-if="plans.length" size="small" class="sec" title="我的套餐">
      <template #header-extra>
        <span v-if="hasQueued" style="font-size:11.5px;color:var(--text-3);">重复购买自动排队，一次只用一份</span>
      </template>
      <!-- 网格而非纵向平铺：每份套餐的信息量固定（一条进度 + 两行小字），窄卡片
           完全放得下，多份并存时横向排开比一路往下堆省掉大半屏高度。
           auto-fill + minmax 让它在窄屏自动退回单列，不用另写断点。 -->
      <div class="plan-grid">
        <template v-for="line in visibleLines" :key="line.key">
          <!-- 只买过一份：还是原来那张卡，信息量没变就别换样子 -->
          <div v-if="line.all.length === 1" class="plan-row" :class="{ queued: line.segs[0].status === 'queued' }">
            <div style="display:flex;justify-content:space-between;align-items:center;gap:6px;margin-bottom:6px;">
              <span style="font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">{{ line.name }}</span>
              <n-tag :type="planStatus(line.segs[0]).type" size="small" bordered>{{ planStatus(line.segs[0]).label }}</n-tag>
            </div>
            <n-progress v-if="line.segs[0].status !== 'queued'" type="line" :percentage="planPct(line.segs[0])" :color="planPct(line.segs[0])>90?'#b6413a':'#4f8366'" />
            <div v-else class="pl-stripe"></div>
            <div style="display:flex;justify-content:space-between;font-size:11px;color:var(--text-3);margin-top:4px;gap:8px;">
              <span>{{ segUsage(line.segs[0]) }}</span>
              <span style="white-space:nowrap;">{{ planTime(line.segs[0]) }}</span>
            </div>
          </div>

          <!-- 同一个续期组买过好几份（续费、买不同时长或新旧商品）：它们是一条订阅线，
               一份结束下一份接上。摊成同名的几张卡片就分不清哪份在用、上一段又
               用掉了多少，所以按时间先后串成一条时间线，每段各自记自己的流量。 -->
          <div v-else class="plan-row line">
            <div class="pl-head">
              <span class="pl-name">{{ line.name }}</span>
              <span class="pl-sub">{{ line.all.length }} 段 · 累计已用 {{ fmtBytes(line.totalUsed) }}</span>
            </div>
            <!-- 展开键在时间线上方：历史段按时间序插在最前，键放下面的话，点开
                 之后内容在按钮上方冒出来，按钮连同下半页一起被推走。 -->
            <n-button v-if="line.hidden || expandedLines.includes(line.key)" size="tiny" quaternary
                      class="pl-more" @click="toggleLine(line.key)">
              {{ expandedLines.includes(line.key) ? '收起历史' : `查看更早的 ${line.hidden} 段` }}
            </n-button>
            <div class="pl-tl">
              <div v-for="p in line.segs" :key="p.id" class="pl-seg" :class="segCls(p)">
                <span class="pl-dot"></span>
                <div class="pl-when">
                  <span v-if="p.name !== line.name" class="pl-seg-name">{{ p.name }}</span>
                  <span class="pl-range">{{ segRange(p) }}</span>
                  <span v-if="p.duration_days" class="pl-len">{{ p.duration_days }} 天</span>
                  <n-tag :type="planStatus(p).type" size="tiny" bordered>{{ planStatus(p).label }}</n-tag>
                </div>
                <n-progress v-if="p.status !== 'queued'" type="line" :percentage="planPct(p)" :height="5"
                            :color="planPct(p)>90?'#b6413a':'#4f8366'" />
                <div v-else class="pl-stripe"></div>
                <div class="pl-use">{{ segUsage(p) }}</div>
              </div>
            </div>
          </div>
        </template>
      </div>
      <!-- 整条线都结束的套餐默认收起：它既不影响现在能用多少，也不影响什么时候
           到期，留在列表里只会跟还在用的混在一起。想看仍然能展开。 -->
      <div v-if="canFoldFinished" class="plan-more">
        <n-button size="tiny" quaternary @click="showFinished = !showFinished">
          {{ showFinished ? '收起已结束的套餐' : `另有 ${finishedLines.length} 个已结束的套餐（已过期 / 已用完）` }}
        </n-button>
      </div>
    </n-card>

    <!-- HTTP/SOCKS5 代理（mixed 节点，不在订阅里，单独复制填入 1Panel/Docker 等） -->
    <n-card v-if="proxies.length" size="small" class="sec" title="HTTP / SOCKS5 代理">
      <template #header-extra><span style="font-size:11px;color:var(--text-3);">可填入 1Panel、Docker、git 等只认 HTTP/SOCKS 代理的地方</span></template>
      <!-- 通用账号：一个账号在所有节点上通用，节点换分组、套餐续费都不会变它。
           放在节点列表之前，因为它是这张卡片里唯一需要保存到别处的东西，下面每个
           节点只剩地址和端口的差别。 -->
      <div v-if="acct" class="px-acct">
        <div class="px-head">
          <span class="px-name">通用账号</span>
          <n-tag v-if="acct.expired" type="error" size="small" bordered>已过期</n-tag>
          <!-- idle：凭据本身没问题，但眼下一个节点都开不了。仍标「所有节点通用」
               等于当面说一句假话，和之前藏起一个能用的账号是同一类错误。 -->
          <n-tag v-else-if="acct.idle" type="warning" size="small" bordered>暂不通用</n-tag>
          <n-tag v-else type="info" size="small" bordered>所有节点通用</n-tag>
          <span style="flex:1;"></span>
          <n-button size="tiny" @click="openEditProxy({ ...acct, account: true, custom: true })">编辑账号</n-button>
        </div>
        <div class="pxrow"><span class="pxk">用户名</span><div class="pxv"><n-input-group><n-input :value="acct.username" readonly size="small" /><n-button size="small" @click="copy(acct.username)">复制</n-button></n-input-group></div></div>
        <div class="pxrow"><span class="pxk">密码</span><div class="pxv"><n-input-group><n-input :value="acct.password" type="password" show-password-on="click" readonly size="small" /><n-button size="small" @click="copy(acct.password)">复制</n-button></n-input-group></div></div>
        <div class="pxrow"><span class="pxk">有效期</span><span style="font-size:12px;color:var(--text-2);flex:1;">{{ acct.expires_at ? fmtDate(acct.expires_at) : '永久' }}</span></div>
        <div class="px-hint">
          <template v-if="acct.expired">已过期，下面的节点暂时改用各自套餐的账号。点「编辑账号」续期即可恢复通用。</template>
          <template v-else-if="acct.idle">你名下暂时没有生效中的付费套餐，这个账号目前不会下发到任何节点，填哪儿都连不上。下面的节点请各自展开「详情」，用它自己的「套餐账号」。买一份或续上一份套餐后，它会自动恢复通用（免费分组的节点始终用各自的账号，免费流量不计入付费套餐）。</template>
          <template v-else>与登录账号无关，可随时改。<template v-if="acct.meter_plan">这些节点的代理流量统一计入「{{ acct.meter_plan }}」——多份套餐并存时记的是最早到期的那一份，那份用完或到期后会自动换到下一份。</template></template>
        </div>
      </div>
      <!-- 默认只留一行：节点 + 地址:端口 + 复制链接。原先六行「标签 + 只读输入框 +
           复制」每个代理就吃掉大半屏，而有了整串 URL，逐字段复制只在 1Panel 这类
           分字段表单里才需要——那是少数情况，收进「详情」里按需展开。 -->
      <div v-for="p in proxies" :key="p.tag" class="proxy-row">
        <div class="px-head">
          <span class="px-name">{{ p.tag }}</span>
          <n-tag v-if="p.expired" type="error" size="small" bordered>已过期</n-tag>
          <n-tag :type="p.tls?'success':'warning'" size="small" bordered>{{ p.tls ? 'HTTPS' : 'HTTP / SOCKS5' }}</n-tag>
          <code class="px-addr">{{ p.host }}:{{ p.port }}</code>
          <!-- 一键复制成 scheme://user:pass@host:port —— 大多数工具（git、docker、
               curl、各类 SDK 的 HTTPS_PROXY）只认这一种整串形式。
               只给按钮不显示明文：URL 里带着密码，直接铺在页面上就把详情里那个
               掩码输入框的意义抵消了。
               按钮自成一组：直接摊在 px-head 里的话，窄屏换行会把它们拆散到两行的
               两端（第一行末尾一个、第二行开头一个），整组一起换行才读得出是一组。 -->
          <div class="px-actions">
            <!-- 标签不带「链接」二字：窄屏上这一行本来就要换行，少几个字就少一行，
                 而卡片标题 + 下方说明已经交代了复制到手的是整串 URL。 -->
            <template v-if="p.tls">
              <n-button size="tiny" type="primary" secondary @click="copy(proxyUrl(p, 'https'))">复制 HTTPS</n-button>
            </template>
            <template v-else>
              <n-button size="tiny" type="primary" secondary @click="copy(proxyUrl(p, 'http'))">复制 HTTP</n-button>
              <n-button size="tiny" type="primary" secondary @click="copy(proxyUrl(p, 'socks5'))">复制 SOCKS5</n-button>
            </template>
            <n-button size="tiny" quaternary @click="toggleProxyDetail(p.tag)">
              {{ expandedProxies.includes(p.tag) ? '收起' : '详情' }}
            </n-button>
          </div>
        </div>
        <!-- 默认账号是个待办事项，不该只在展开后才看得见。看的是这个节点实际递出去
             的那一套：用通用账号时它一定是自设过的，只有回落到套餐账号才需要催。 -->
        <div v-if="!p.custom" class="px-hint">系统默认账号，建议点「详情 → 编辑账号」自设</div>
        <div v-if="expandedProxies.includes(p.tag)" class="px-detail">
          <div class="pxrow"><span class="pxk">类型</span><span style="font-size:13px;">{{ p.tls ? 'HTTPS' : 'HTTP / SOCKS5' }}</span></div>
          <div class="pxrow"><span class="pxk">地址</span><div class="pxv"><n-input-group><n-input :value="p.host" readonly size="small" /><n-button size="small" @click="copy(p.host)">复制</n-button></n-input-group></div></div>
          <div class="pxrow"><span class="pxk">端口</span><div class="pxv"><n-input-group><n-input :value="String(p.port)" readonly size="small" /><n-button size="small" @click="copy(String(p.port))">复制</n-button></n-input-group></div></div>
          <!-- 两套账号在这个节点上都能登录，所以两套都列出来。通用账号只用一行指
               回上面——它每个节点都一样，逐个节点再抄一遍反而像「一节点一套」；套
               餐账号则必须逐项列全，它是这个节点独有的，不列就等于没有。 -->
          <div v-if="p.account" class="pxrow">
            <span class="pxk">通用账号</span>
            <span class="pxsub">上方那一个，所有节点相同；「复制 HTTP / SOCKS5」拿到的就是它</span>
          </div>
          <div v-if="p.plan" class="px-plan">
            <div class="px-head">
              <span class="px-name">套餐账号<template v-if="p.plan.name"> · {{ p.plan.name }}</template></span>
              <n-tag v-if="p.plan.expired" type="error" size="small" bordered>已过期</n-tag>
              <span style="flex:1;"></span>
              <n-button size="tiny" @click="openEditProxy(p.plan)">编辑账号</n-button>
            </div>
            <div class="pxrow"><span class="pxk">用户名</span><div class="pxv"><n-input-group><n-input :value="p.plan.username" readonly size="small" /><n-button size="small" @click="copy(p.plan.username)">复制</n-button></n-input-group></div></div>
            <div class="pxrow"><span class="pxk">密码</span><div class="pxv"><n-input-group><n-input :value="p.plan.password" type="password" show-password-on="click" readonly size="small" /><n-button size="small" @click="copy(p.plan.password)">复制</n-button></n-input-group></div></div>
            <div class="pxrow">
              <span class="pxk">有效期</span>
              <span class="pxsub">{{ p.plan.expires_at ? fmtDate(p.plan.expires_at) : '永久' }}</span>
            </div>
            <div class="pxrow" v-if="p.account">
              <span class="pxk">整串</span>
              <div class="pxv px-actions">
                <template v-if="p.tls">
                  <n-button size="tiny" secondary @click="copy(proxyUrl({ ...p, ...p.plan }, 'https'))">复制 HTTPS</n-button>
                </template>
                <template v-else>
                  <n-button size="tiny" secondary @click="copy(proxyUrl({ ...p, ...p.plan }, 'http'))">复制 HTTP</n-button>
                  <n-button size="tiny" secondary @click="copy(proxyUrl({ ...p, ...p.plan }, 'socks5'))">复制 SOCKS5</n-button>
                </template>
              </div>
            </div>
            <div class="px-hint">
              <template v-if="!p.plan.custom">系统默认账号，点「编辑账号」可自设。</template>
              只在这个节点所属的「{{ p.plan.name || '本份套餐' }}」上有效，流量也记在它名下。
              <template v-if="p.account && p.meter_plan && p.meter_plan !== p.plan.name">想把这个节点的代理流量记在「{{ p.plan.name }}」而不是「{{ p.meter_plan }}」，就用这一套。</template>
            </div>
          </div>
        </div>
      </div>
      <!-- 两套账号并排讲一次取舍。分开写在各自的提示句里时，每句单独看都对，但
           「我到底该用哪个」从头到尾没有一个地方回答过。默认收起：已经知道的人不
           必每次都看，困惑的人在这张卡片底部找得到。 -->
      <div class="px-choose">
        <n-button size="tiny" quaternary @click="showChoose = !showChoose">
          {{ showChoose ? '收起说明' : '两套账号怎么选？' }}
        </n-button>
        <div v-if="showChoose" class="px-choose-body">
          <div class="pc-row">
            <span class="pc-k">通用账号</span>
            <span class="pc-v">所有节点同一套，节点换分组、套餐续费都不会变它。流量汇总记在一份上——多份套餐并存时是<b>最早到期</b>的那份，那份用完或到期后自动换到下一份<template v-if="acct && acct.meter_plan">（目前是「{{ acct.meter_plan }}」）</template>。图省事、一处填遍所有节点就用它。</span>
          </div>
          <div class="pc-row">
            <span class="pc-k">套餐账号</span>
            <span class="pc-v">每份套餐各一套，只在这份套餐拥有的节点上有效。流量<b>精确</b>记在它自己那份上，不做任何推断。想让某个节点的流量算在指定套餐头上，就展开那个节点的「详情」用它。</span>
          </div>
          <div class="pc-note">两套都有效，可以混着用：同一个节点上你填哪套，流量就按哪套的规则记。</div>
        </div>
      </div>
      <div style="font-size:11px;color:var(--text-3);margin-top:10px;">命令行 / Docker / git 等直接点「复制 HTTP」「复制 SOCKS5」，拿到的是 <code>scheme://用户名:密码@地址:端口</code> 整串。1Panel 这类分字段的表单展开「详情」逐项复制：代理类型选 <b>HTTP</b> 或 <b>SOCKS5</b>（标着 <b>HTTPS</b> 的节点则选 HTTPS）。</div>
    </n-card>

    <!-- 编辑代理账号 -->
    <n-modal v-model:show="showEditProxy" preset="card" title="编辑代理账号" style="max-width:440px;">
      <n-form label-placement="left" label-width="72">
        <n-form-item label="适用范围">
          <n-input :value="editForm.scope" readonly />
        </n-form-item>
        <n-form-item label="用户名">
          <n-input v-model:value="editForm.username" placeholder="仅字母/数字/ _.@- ，不能以 qz_ 开头" />
        </n-form-item>
        <n-form-item label="密码">
          <n-input-group>
            <n-input v-model:value="editForm.password" type="password" show-password-on="click" placeholder="6-128 位" />
            <n-button @click="genProxyPassword">生成32位</n-button>
          </n-input-group>
        </n-form-item>
        <n-form-item label="有效期">
          <div style="width:100%;">
            <n-switch v-model:value="editForm.permanent" style="margin-bottom:8px;"><template #checked>永久</template><template #unchecked>指定日期</template></n-switch>
            <n-date-picker v-if="!editForm.permanent" v-model:value="editForm.expireTs" type="datetime" clearable style="width:100%;" />
          </div>
        </n-form-item>
        <div style="font-size:11px;color:var(--text-3);margin-bottom:12px;">
          这是仅用于该协议的独立账号，与登录账号无关。密码泄露可随时来此更改；到期后该代理自动停用（可续期）。
          <template v-if="editForm.account">改的是所有节点通用的那一个，保存后旧密码立即失效，记得同步更新已填在别处的地方。</template>
          <template v-else>改的是这一份套餐自己的那一个，通用账号不受影响。</template>
        </div>
        <n-button type="primary" block :loading="savingProxy" @click="saveProxy">保存</n-button>
      </n-form>
    </n-modal>

    <!-- 节点列表 -->
    <n-card size="small" class="sec" title="节点列表">
      <template #header-extra>
        <n-space size="small">
          <n-input v-model:value="search" placeholder="搜索节点 / 线路" size="small" style="width:160px;" clearable />
          <n-select v-model:value="protoFilter" :options="protoOptions" placeholder="协议" size="small" style="width:100px;" clearable />
          <n-button size="small" @click="handlePing" :loading="pinging">测速</n-button>
          <n-button size="small" @click="handleToggleAll(true)">全启用</n-button>
          <n-button size="small" @click="handleToggleAll(false)">全禁用</n-button>
        </n-space>
      </template>
      <!-- 按套餐分节：一个节点归哪份套餐，决定的是它走谁的流量和有效期，所以这条
           归属线才是节点列表真正的分组依据（后端 plan_id 就是计费用的那个桶）。 -->
      <div v-for="g in nodeGroups" :key="g.planId" class="ngrp">
        <div class="ngrp-head">
          <span class="ngrp-name">{{ g.planName }}</span>
          <span class="ngrp-meta">{{ g.nodes.length }} 个节点<template v-if="g.offCount"> · {{ g.offCount }} 个禁用</template></span>
          <span style="flex:1;"></span>
          <n-button size="tiny" quaternary @click="handlePlanToggle(g, true)">全启用</n-button>
          <n-button size="tiny" quaternary @click="handlePlanToggle(g, false)">全禁用</n-button>
        </div>
        <n-data-table :columns="nodeCols" :data="g.nodes" :bordered="false" size="small"
                      :pagination="g.nodes.length > 20 ? { pageSize: 20 } : false" :row-key="(r:any)=>r.key"
                      :checked-row-keys="selectedByPlan[g.planId] || []"
                      @update:checked-row-keys="(k:any) => selectedByPlan[g.planId] = k" />
      </div>
      <n-empty v-if="!loadingNodes && !nodeGroups.length" :description="nodes.length ? '没有匹配的节点' : '暂无节点'" style="padding:28px 0;" />
      <div v-if="selectedKeys.length" style="margin-top:10px;display:flex;gap:8px;align-items:center;">
        <span style="font-size:12px;color:var(--text-3);">已选 {{ selectedKeys.length }} 个</span>
        <n-button size="small" type="primary" @click="handleBulk(true)">批量启用</n-button>
        <n-button size="small" type="error" @click="handleBulk(false)">批量禁用</n-button>
      </div>
      <div style="margin-top:8px;font-size:11px;color:var(--text-3);">共 {{ nodes.length }} 个节点，{{ filteredNodes.length }} 个匹配，{{ nodes.filter((n:any)=>n.disabled).length }} 个禁用。线路一栏从左到右是客户端实际走的路径：入口机 → 中转机 → 出网。</div>
    </n-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, h, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { NCard, NInput, NInputGroup, NButton, NDataTable, NTag, NTooltip, NSelect, NSpace, NModal, NForm, NFormItem, NSwitch, NDatePicker, NProgress, NIcon, NEmpty, useMessage, useDialog } from 'naive-ui'
import { SpeedometerOutline } from '@vicons/ionicons5'
import { apiGet, apiList, apiPost, apiPut } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { fmtBytes, fmtTotal, fmtDate, pct } from '@/utils/format'
import { planStatusMeta, planTimeText, planSortKey } from '@/utils/plan'
import { copyText } from '@/utils/clipboard'
import QRCode from 'qrcode'

const router = useRouter()
const message = useMessage()
const dialog = useDialog()
const auth = useAuthStore()
const sub = ref<any>({})
const proxies = ref<any[]>([])
const nodes = ref<any[]>([])
const routingProfile = ref<'cn_direct' | 'proxy_all'>('cn_direct')
const routingProfileOptions = [
  { label: '智能分流（推荐）', value: 'cn_direct' },
  { label: '全部代理', value: 'proxy_all' },
]
const routingProfileParam = computed(() => routingProfile.value === 'cn_direct' ? 'cn-direct' : 'proxy-all')
// Build every native-config URL from the current base address and current selector
// value. This avoids a stale/missing `profiles` payload making the visible choice
// and the copied Clash/sing-box/Surge link disagree during rolling upgrades.
function routedSubscriptionURL(format?: 'clash' | 'singbox' | 'surge'): string {
  const base = sub.value?.url
  if (!base) return ''
  try {
    const u = new URL(base, window.location.origin)
    u.searchParams.set('profile', routingProfileParam.value)
    if (format) u.searchParams.set('format', format)
    return u.toString()
  } catch { return '' }
}
const selectedSubscriptionURL = computed(() => routedSubscriptionURL())
const selectedFormats = computed(() => ({
  clash: routedSubscriptionURL('clash'),
  singbox: routedSubscriptionURL('singbox'),
  surge: routedSubscriptionURL('surge'),
}))
// v2rayN/base64 subscriptions contain share links only and have no routing-policy
// field. Keep its URL format-pinned but do not pretend the CN selector can alter it.
const base64SubscriptionURL = computed<string>(() => sub.value?.formats?.base64 || sub.value?.formats?.default || '')
const routingProfileNote = computed(() => routingProfile.value === 'cn_direct'
  ? 'AI 走代理，中国大陆直连，其余公网走代理；Clash / sing-box 同步使用国内解析，Surge 遵循系统 DNS。'
  : 'AI 和所有公网流量都走代理，仅局域网保持直连。')
// 我的套餐：后端按套餐独立计量（可能多份并存、含排队份），全部列出，不合并
const plans = ref<any[]>([])
const activePlanCount = computed(() => plans.value.filter(p => p.status === 'active').length)
const queuedPlanCount = computed(() => plans.value.filter(p => p.status === 'queued').length)
const enabledNodeCount = computed(() => nodes.value.filter(n => !n.disabled).length)
const disabledNodeCount = computed(() => nodes.value.filter(n => n.disabled).length)
// Read: 使用中 first, then 排队中 (by soonest activation), then finished — so the
// current份 and what's next are always at the top.
const sortedPlans = computed<any[]>(() => {
  const list = [...plans.value]
  list.sort((a, b) => planSortKey(a) - planSortKey(b) || (a.activate_by || a.expiry_at || 0) - (b.activate_by || b.expiry_at || 0))
  return list
})
const hasQueued = computed(() => plans.value.some(p => p.status === 'queued'))

// ---- 订阅线：同一个续期组的若干份合成一条时间线 ----
//
// 后端按份独立计量，续费或买了不同时长就会有好几份同名的份并存。它们在时间上
// 是首尾相接的一条线（一份用完/到期，下一份自动接上），所以按续期组归组、组内按
// 时间先后排，比平铺几张同名卡片好读得多，也才能让每一段各自显示自己的用量。
// 不属于套餐的份（流量池、管理员赠送）没有这种接续关系，各自成一条单段线。
type PlanLine = {
  key: string
  name: string
  all: any[]       // 这条线的全部份，已按时间先后排好
  segs: any[]      // 当前显示的段（默认不含已结束的）
  hidden: number   // 被收起的历史段数
  finished: boolean
  totalUsed: number
}
// 一段落在时间线的哪一档：已结束的在前（过去），使用中居中，排队的在后（将来）。
function chronoKey(p: any): number {
  const s = planSortKey(p)
  return s === 2 ? 0 : s === 0 ? 1 : 2
}
// 这一段是什么时候开始的。started_at 由后端从「到期时间 − 时长」还原；拿不到就
// 退回发放时间（老数据 / 不过期的份）。
function segStart(p: any): number { return p.started_at || p.created_at || 0 }

const expandedLines = ref<string[]>([])
function toggleLine(key: string) {
  const i = expandedLines.value.indexOf(key)
  if (i >= 0) expandedLines.value.splice(i, 1)
  else expandedLines.value.push(key)
}

const planLines = computed<PlanLine[]>(() => {
  const byKey = new Map<string, any[]>()
  // 用 sortedPlans 的顺序建组，于是组的先后仍是「使用中的套餐在前」。
  for (const p of sortedPlans.value) {
    const key = p.kind === 'plan' && p.package_id > 0 ? (p.queue_key || 'pkg:' + p.package_id) : 'one:' + p.id
    const arr = byKey.get(key)
    if (arr) arr.push(p)
    else byKey.set(key, [p])
  }
  const out: PlanLine[] = []
  for (const [key, arr] of byKey) {
    const all = [...arr].sort((a, b) =>
      chronoKey(a) - chronoKey(b) ||
      (segStart(a) || a.activate_by || 0) - (segStart(b) || b.activate_by || 0) ||
      a.id - b.id)
    const finished = all.every(p => planSortKey(p) === 2)
    // 整条线都结束时不再收起段落——那张卡片本来就是历史，收了就没内容了。
    const segs = finished || expandedLines.value.includes(key) ? all : all.filter(p => planSortKey(p) !== 2)
    out.push({
      key, name: all[0].name || '套餐 #' + all[0].id, all, segs,
      hidden: all.length - segs.length, finished,
      totalUsed: all.reduce((n, p) => n + (p.used || 0), 0),
    })
  }
  return out
})
// 整条线都已结束的套餐默认收起；全部套餐都结束时不折叠，否则列表会整块空掉，
// 看不出服务为什么停了。
const finishedLines = computed(() => planLines.value.filter(l => l.finished))
const canFoldFinished = computed(() => finishedLines.value.length > 0 && finishedLines.value.length < planLines.value.length)
const showFinished = ref(false)
const visibleLines = computed<PlanLine[]>(() =>
  showFinished.value || !canFoldFinished.value ? planLines.value : planLines.value.filter(l => !l.finished))

function segCls(p: any) {
  const k = planSortKey(p)
  return { fin: k === 2, now: k === 0, q: k === 1 }
}
// 一段的时间区间。排队中的还没开始，只能给个预计生效时间。
function segRange(p: any): string {
  if (p.status === 'queued') {
    return p.activate_by ? `预计 ${fmtDate(p.activate_by)} 起` : '前一段结束后开始'
  }
  const start = segStart(p)
  const end = p.expiry_at ? fmtDate(p.expiry_at) : '不过期'
  return start ? `${fmtDate(start)} → ${end}` : end
}
// 一段用掉了多少。排队中的还没开始计量，只报待用额度。
function segUsage(p: any): string {
  if (p.status === 'queued') return '待用流量 ' + fmtTotal(p.traffic_limit)
  if (p.traffic_limit <= 0) return `已用 ${fmtBytes(p.used)} / 0 B · 剩 0 B`
  return `已用 ${fmtBytes(p.used)} / ${fmtTotal(p.traffic_limit)} · 剩 ${fmtBytes(p.remaining < 0 ? 0 : p.remaining)}`
}
function planPct(p: any) { return p.status === 'queued' ? 0 : pct(p.used, p.traffic_limit) }
const planStatus = planStatusMeta
const planTime = (p: any) => planTimeText(p, fmtDate)
// 由后端的 node_creds_reset_enabled 决定，缺省按关闭处理——拿不到就当没开，
// 不要给用户一个点了必然 403 的按钮。
const credsResetEnabled = computed(() => sub.value?.creds_reset_enabled === true)
const resettingCreds = ref(false)

// 代理账号编辑
const showEditProxy = ref(false)
const savingProxy = ref(false)
const editForm = ref<any>({ account: false, bucket_id: 0, scope: '', username: '', password: '', permanent: true, expireTs: null as number | null })

// 通用账号。单独取而不是从节点行里挑：过期后节点行会退回各自套餐的账号，
// 从行里推就再也看不见它，也就没法续期了。
const acct = ref<any | null>(null)
async function loadProxies() {
  proxies.value = await apiList('/api/user/proxies')
  try { acct.value = await apiGet('/api/user/proxy-account') } catch { acct.value = null }
}

// 「两套账号怎么选」的展开状态。
const showChoose = ref(false)

// 展开的代理（按 tag 记）。默认全收起——常用路径是复制整串 URL，逐字段只是备用。
const expandedProxies = ref<string[]>([])
function toggleProxyDetail(tag: string) {
  const i = expandedProxies.value.indexOf(tag)
  if (i >= 0) expandedProxies.value.splice(i, 1)
  else expandedProxies.value.push(tag)
}

// proxyUrl 拼出 scheme://user:pass@host:port。
// - 用户名/密码走 encodeURIComponent：密码是用户自设的任意 6-128 位，里面出现
//   @ : / # ? 都会把 URL 解析歪，必须转义。
// - host 若是裸 IPv6（含 :）要加方括号，否则冒号会被当成端口分隔符。
function proxyUrl(p: any, scheme: string) {
  const host = p.host?.includes(':') && !p.host.startsWith('[') ? `[${p.host}]` : p.host
  const cred = p.username ? `${encodeURIComponent(p.username)}:${encodeURIComponent(p.password || '')}@` : ''
  return `${scheme}://${cred}${host}:${p.port}`
}

function openEditProxy(p: any) {
  editForm.value = {
    account: !!p.account,
    bucket_id: p.bucket_id,
    // 「适用范围」不写节点名：按份的凭据在这份套餐拥有的每个节点上都认，只写当前
    // 这个节点会让人以为改它只影响这一个。
    scope: p.account ? '所有节点（通用账号）' : `「${p.name || '本份套餐'}」拥有的所有节点`,
    // 默认用登录账号名（仅作默认，是独立的代理账号）；已自设过则回填现有用户名。
    username: p.custom ? p.username : (auth.user?.username || ''),
    // 回填现有密码：只改有效期或用户名的人不该被迫换一次密码——密码一换，所有
    // 已经填在 1Panel/Docker 里的地方就当场断了。页面上本来就显示着它，回填不多
    // 暴露任何东西。
    password: p.password || '',
    permanent: !p.expires_at,
    expireTs: p.expires_at ? p.expires_at * 1000 : null,
  }
  showEditProxy.value = true
}

function genProxyPassword() {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  const arr = new Uint32Array(32)
  crypto.getRandomValues(arr)
  editForm.value.password = Array.from(arr, (n) => chars[n % chars.length]).join('')
}

async function saveProxy() {
  const f = editForm.value
  if (!f.username?.trim()) { message.warning('请填写用户名'); return }
  if (!f.password || f.password.length < 6) { message.warning('密码至少 6 位'); return }
  if (!f.permanent && !f.expireTs) { message.warning('请选择有效期，或切换为永久'); return }
  savingProxy.value = true
  try {
    const body = {
      username: f.username.trim(),
      password: f.password,
      expires_at: f.permanent ? 0 : Math.floor(f.expireTs / 1000),
    }
    await apiPut(f.account ? '/api/user/proxy-account' : '/api/user/proxies/' + f.bucket_id, body)
    message.success('已保存，代理账号已更新')
    showEditProxy.value = false
    await loadProxies()
  } catch (e: any) { message.error(e.message) } finally { savingProxy.value = false }
}
const showQr = ref(false)
const qrCanvas = ref<HTMLCanvasElement | null>(null)
const search = ref('')
const protoFilter = ref<string | null>(null)
// 每个套餐一张表，勾选状态也得一张表一份：几张表共用一个 ref 时，任何一张表的
// update 事件都会把自己那份完整勾选列表写回去，等于清空其他表的选择。
const selectedByPlan = ref<Record<string, string[]>>({})
// 去重：一条线路可以同时属于两份套餐，两个分组里都勾上就会出现两次同一个 key。
const selectedKeys = computed<string[]>(() => [...new Set(Object.values(selectedByPlan.value).flat())])
const loadingNodes = ref(false)
const pinging = ref(false)

const protoOptions = computed(() => {
  const set = new Set(nodes.value.map((n: any) => n.protocol).filter(Boolean))
  return Array.from(set).map(p => ({ label: p.toUpperCase(), value: p }))
})

const filteredNodes = computed(() => {
  let list: any[] = nodes.value
  if (search.value) {
    const q = search.value.toLowerCase()
    // 节点名和链路上的机器名/地区都匹配：两列都在眼前，搜哪个都该命中。
    list = list.filter((n: any) => [
      n.name, n.server,
      ...(n.topo?.hops || []).flatMap((h: any) => [h.name, h.location]),
    ].some((s: any) => s?.toLowerCase().includes(q)))
  }
  if (protoFilter.value) list = list.filter((n: any) => n.protocol === protoFilter.value)
  return list
})

// 分组顺序跟着「我的套餐」卡片走（使用中在前、排队在后），两处对同一份套餐的排序
// 一致，滚上去核对时不用重新找。套餐列表里没有的归属（免费线路）排在最后。
//
// 后端给的是「哪几份套餐能用这条线路」，一条线路可能被两份套餐同时覆盖，那它就在
// 两个分组里各出现一次——这是事实，不是重复。节点对象是同一个引用，所以在任一处
// 开关，另一处的状态跟着变。
const nodeGroups = computed(() => {
  const order = new Map<string, number>()
  sortedPlans.value.forEach((p, i) => order.set(String(p.id), i))
  const byPlan = new Map<string, any>()
  for (const n of filteredNodes.value) {
    const refs = n.plans?.length ? n.plans : [{ id: 0, name: '未归属套餐' }]
    for (const ref of refs) {
      const id = String(ref.id)
      let g = byPlan.get(id)
      if (!g) {
        g = { planId: id, planName: ref.name, nodes: [] as any[], offCount: 0 }
        byPlan.set(id, g)
      }
      g.nodes.push(n)
      if (n.disabled) g.offCount++
    }
  }
  return [...byPlan.values()].sort((a, b) =>
    (order.get(a.planId) ?? Number.MAX_SAFE_INTEGER) - (order.get(b.planId) ?? Number.MAX_SAFE_INTEGER))
})

function latencyColor(ms: number) {
  if (ms < 0) return 'var(--text-3)'
  if (ms < 150) return '#10b981'
  if (ms < 400) return '#bf9540'
  return '#ef4444'
}

// 一段链路的胶囊。这些 vnode 由 n-data-table 渲染，拿不到本组件 scoped 样式的
// 属性标记，所以类名走文件末尾那个非 scoped 的 style 块。
function hopChip(kind: string, name: string, proto?: string, loc?: string) {
  return h('span', { class: 'qz-hop qz-hop-' + kind, title: loc || undefined }, [
    h('b', null, name),
    proto ? h('span', { class: 'qz-hop-proto' }, proto.toUpperCase()) : null,
  ])
}

// 「哪台机器 → 哪台机器」。这一栏只画路径，不重复节点名——名字在左边独立一列，
// 因为它是唯一能和客户端里那条订阅对上号的标识，路径本身回答的是另一个问题
// （流量实际怎么走），两件事各占一列比挤在一起清楚。
function renderTopo(r: any) {
  const kids: any[] = []
  const hops = r.topo?.hops || []
  if (!hops.length) {
    // 外部导入的分享链接：这条链路不在我们手里，除了它自己什么都不知道。
    kids.push(hopChip('ext', '外部节点', r.protocol))
  } else {
    hops.forEach((hp: any, i: number) => {
      if (i) kids.push(h('span', { class: 'qz-arrow' }, hp.kind === 'egress' ? '⇢ 出口 ⇢' : '⇢ 中转 ⇢'))
      kids.push(hopChip(hp.kind, hp.name, hp.protocol, hp.location))
    })
  }
  // 降级不是细节：落地/出口断了，流量改从上一跳的 IP 出网，出口地址静默变了。
  // 这段本身就带箭头，末尾不再补一个，免得出现「⇢ … ⇢ →」。
  if (r.topo?.warn) {
    kids.push(h('span', { class: 'qz-arrow qz-arrow-warn' },
      r.topo.warn === 'egress' ? '⇢ 出口已失效 ⇢' : '⇢ 落地已失效 ⇢'))
  } else {
    kids.push(h('span', { class: 'qz-arrow' }, '→'))
  }
  kids.push(h('span', { class: 'qz-hop qz-hop-inet' }, '🌐 互联网'))
  return h('div', { class: 'qz-topo', title: r.name || '' }, kids)
}

const nodeCols = [
  { type: 'selection' as const },
  // 节点名单独一列而不是塞进线路里：客户端（Clash / v2rayN）节点选择器上显示的就是
  // 这个名字，没有它就对不上「我现在连的是哪条」。宽度写死 + 省略号，免得长名字
  // 把右边的线路挤没。
  { title: '节点', key: 'name', width: 132, ellipsis: { tooltip: true } },
  { title: '线路', key: 'topo', render: renderTopo },
  {
    title: '延迟', key: 'latency', width: 70,
    render: (r: any) => {
      if (r.latency == null) return h('span', { style: 'color:var(--text-3)' }, '—')
      if (r.latency < 0) return h(NTag, { size: 'tiny', type: 'error', bordered: false }, { default: () => '超时' })
      return h('span', { style: `color:${latencyColor(r.latency)};font-weight:600;` }, r.latency + 'ms')
    },
  },
  {
    title: '状态', key: 'disabled', width: 70,
    render: (r: any) => h(NTag, { type: r.disabled ? 'default' : 'success', size: 'small', bordered: false }, { default: () => r.disabled ? '禁用' : '启用' }),
  },
  {
    title: '操作', key: 'act', width: 70,
    render: (r: any) => h(NButton, { size: 'tiny', onClick: () => toggleNode(r) }, { default: () => r.disabled ? '启用' : '禁用' }),
  },
]

async function toggleNode(node: any) {
  try {
    const newDisabled = !node.disabled
    await apiPost('/api/user/nodes/toggle', { key: node.key, disabled: newDisabled })
    node.disabled = newDisabled
  } catch (e: any) { message.error(e.message) }
}
async function handlePing() {
  pinging.value = true
  try {
    const data = await apiList<any>('/api/user/nodes/ping')
    const m = new Map(data.map((d: any) => [d.key, d.latency]))
    nodes.value = nodes.value.map((n: any) => ({ ...n, latency: m.get(n.key) ?? null }))
    message.success('测速完成')
  } catch (e: any) { message.error(e.message) } finally { pinging.value = false }
}
// applyBulk 是「一批节点一起开/关」的唯一路径：批量按钮和每个套餐的全启用/全禁用
// 都走它，避免两处各写一遍本地状态回写。
async function applyBulk(keys: string[], enable: boolean) {
  if (!keys.length) return
  const body = enable ? { enable: keys, disable: [] } : { enable: [], disable: keys }
  await apiPost('/api/user/nodes/bulk', body)
  const set = new Set(keys)
  nodes.value = nodes.value.map((n: any) => set.has(n.key) ? { ...n, disabled: !enable } : n)
}
async function handleBulk(enable: boolean) {
  try {
    await applyBulk(selectedKeys.value, enable)
    selectedByPlan.value = {}
    message.success(enable ? '已批量启用' : '已批量禁用')
  } catch (e: any) { message.error(e.message) }
}
// 只动这份套餐下的节点——分组之后「全启用」按钮就在分组标题里，它要是仍然扫全部
// 节点，点下去会连别的套餐一起改，和它所在的位置说的不是一回事。
async function handlePlanToggle(g: any, enable: boolean) {
  try {
    await applyBulk(g.nodes.map((n: any) => n.key), enable)
    message.success(`「${g.planName}」已全部${enable ? '启用' : '禁用'}`)
  } catch (e: any) { message.error(e.message) }
}
async function handleToggleAll(enable: boolean) {
  try {
    await apiPost(enable ? '/api/user/nodes/enable-all' : '/api/user/nodes/disable-all')
    nodes.value = nodes.value.map((n: any) => ({ ...n, disabled: !enable }))
    message.success(enable ? '已全部启用' : '已全部禁用')
  } catch (e: any) { message.error(e.message) }
}
function handleResetSub() {
  // Swapping the address invalidates every client configured with the old one,
  // so it still needs a confirm — but it does NOT revoke the nodes those clients
  // already hold, and the copy must not claim otherwise.
  dialog.warning({
    title: '确认更换订阅地址',
    content: '更换后旧地址立即失效，所有已导入的客户端（Clash / sing-box 等）都需要用新地址重新导入。注意：已经从旧地址导出的节点仍然可用，如需彻底吊销请联系管理员重置节点凭据。确定更换？',
    positiveText: '更换', negativeText: '取消',
    onPositiveClick: async () => {
      try { await apiPost('/api/user/reset-sub'); sub.value = await apiGet('/api/user/subscription') || {}; message.success('订阅地址已更换') }
      catch (e: any) { message.error(e.message) }
    },
  })
}
function handleResetNodeCreds() {
  // The expensive half. Unlike the address swap this one has a real cost to
  // spell out: it needs a node-side push before it takes effect, it breaks every
  // client until they re-import, and it can only be done once a month.
  dialog.error({
    title: '确认重置节点凭据',
    content: '这会为你的所有节点重新生成凭据，从旧订阅导出的节点将彻底失效。'
      + '新凭据需要同步到各节点后才生效（通常 1 分钟内），期间你自己的连接也会中断，需要重新导入订阅。'
      + '每 30 天只能重置一次。确定重置？',
    positiveText: '重置', negativeText: '取消',
    onPositiveClick: async () => {
      resettingCreds.value = true
      try {
        const r: any = await apiPost('/api/user/reset-node-creds')
        sub.value = await apiGet('/api/user/subscription') || {}
        // 节点链接与代理账号都嵌了刚刚轮换掉的凭据，必须重新拉一次，
        // 否则页面上还挂着一份已经失效的旧链接。
        try { nodes.value = await apiList('/api/user/nodes') } catch {}
        try { await loadProxies() } catch {}
        const secs = Number(r?.applies_in_seconds) || 60
        message.success(`节点凭据已重置，约 ${secs} 秒后在各节点生效，请重新导入订阅`)
      } catch (e: any) { message.error(e.message) }
      finally { resettingCreds.value = false }
    },
  })
}
async function copy(text: string) {
  if (!text) { message.warning('暂无链接'); return }
  // Honest feedback: navigator.clipboard is unavailable on plain-HTTP origins
  // (common for self-hosted panels), so copyText falls back and reports failure
  // rather than us claiming success on a silent no-op.
  if (await copyText(text)) message.success('已复制'); else message.error('复制失败，请手动选择并复制')
}

watch([showQr, selectedSubscriptionURL], async ([visible, url]) => {
  if (visible && url) {
    await nextTick()
    if (qrCanvas.value) {
      QRCode.toCanvas(qrCanvas.value, url, { width: 180, margin: 2 }, (err: any) => {
        if (err) console.error('QR error:', err)
      })
    }
  }
})

onMounted(async () => {
  try { sub.value = await apiGet('/api/user/subscription') || {} } catch (e: any) { message.error('订阅信息加载失败：' + (e?.message || '请稍后重试')) }
  try { plans.value = await apiList('/api/user/plans') } catch {}
  try { await loadProxies() } catch {}
  loadingNodes.value = true
  try { nodes.value = await apiList('/api/user/nodes') } catch {} finally { loadingNodes.value = false }
})
</script>

<style scoped>
.page-title { font-size: 21px; margin-bottom: 4px; }
.page-sub { color: var(--text-2); margin-bottom: 0; }
.sub-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 20px; }
.sec { margin-bottom: 16px; border-radius: var(--r-sm); }
.sec-title { font-weight: 650; font-size: 14px; }
.sec-caption { margin-left: 10px; color: var(--text-3); font-size: 11.5px; font-weight: 400; }
.routing-choice { display: grid; grid-template-columns: 58px minmax(180px, 240px) 1fr; align-items: center; gap: 8px; margin-bottom: 10px; }
.routing-choice-label { color: var(--text-3); font-size: 11px; }
.routing-choice-note, .routing-compat-note { color: var(--text-3); font-size: 11px; line-height: 1.6; }
.routing-compat-note { margin-top: 5px; }
.sub-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; margin-bottom: 16px; }
.sub-stat { min-width: 0; padding: 12px 14px; border: 1px solid var(--border); border-radius: 12px; background: var(--card); box-shadow: var(--shadow-sm); }
.sub-stat span, .sub-stat small { display: block; color: var(--text-3); font-size: 11px; }
.sub-stat b { display: block; margin: 3px 0 2px; color: var(--text); font-size: 18px; line-height: 1.2; font-variant-numeric: tabular-nums; }
.sub-action-row { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; margin-top: 11px; }
.sub-action-row.safety { padding-top: 10px; border-top: 1px solid var(--border); }
.sub-action-label { width: 58px; flex: 0 0 58px; color: var(--text-3); font-size: 11px; }
.sub-security-note { margin: 8px 0 0 65px; color: var(--text-3); font-size: 11px; line-height: 1.65; }
.plan-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 10px; }
.plan-row { padding: 12px; background: var(--bg-soft); border-radius: 10px; min-width: 0; }
.plan-row.queued { opacity: .72; border: 1px dashed var(--border); background: transparent; }
.plan-more { margin-top: 8px; }
.plan-more :deep(.n-button) { color: var(--text-3); font-size: 12px; }

/* 订阅线：整行独占，段落纵向串成时间线（左侧一竖线 + 每段一个圆点） */
.plan-row.line { grid-column: 1 / -1; }
.pl-head { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; margin-bottom: 10px; }
.pl-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pl-sub { font-size: 11px; color: var(--text-3); white-space: nowrap; }
.pl-seg { position: relative; padding: 0 0 14px 20px; }
.pl-seg:last-child { padding-bottom: 0; }
/* 连接线从圆点下方一直画到下一段，最后一段不画 */
.pl-seg::before { content: ''; position: absolute; left: 4px; top: 14px; bottom: 0; width: 1px; background: var(--border); }
.pl-seg:last-child::before { display: none; }
.pl-dot { position: absolute; left: 0; top: 4px; width: 9px; height: 9px; border-radius: 50%; background: var(--border); }
.pl-seg.now .pl-dot { background: #6f8f76; box-shadow: 0 0 0 3px rgba(111, 143, 118, .16); }
.pl-seg.q .pl-dot { background: transparent; border: 1px dashed var(--text-3); }
.pl-seg.fin { opacity: .62; }
.pl-when { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-bottom: 5px; }
.pl-seg-name { font-size: 12px; font-weight: 600; color: var(--text); }
.pl-range { font-size: 12px; color: var(--text-2); }
.pl-len { font-size: 11px; color: var(--text-3); border: 1px solid var(--border); border-radius: 999px; padding: 0 6px; }
.pl-use { font-size: 11px; color: var(--text-3); margin-top: 4px; }
.pl-stripe { height: 6px; border-radius: 3px; background: repeating-linear-gradient(45deg, var(--border), var(--border) 4px, transparent 4px, transparent 8px); }
.pl-more { margin: -2px 0 8px; color: var(--text-3); }
/* 通用账号是这张卡片里唯一要抄走的东西，加一道左边线把它和下面的节点行分开 */
.px-acct { margin-bottom: 10px; padding: 10px 12px; background: var(--bg-soft); border-radius: 10px; border-left: 3px solid var(--primary, #63e2b7); }
.px-acct .pxrow { margin-top: 8px; }
/* 节点详情里的套餐账号：缩进一块，读起来是「这个节点还有另一套账号」而不是又一
   组并列字段 */
.px-plan { margin-top: 10px; padding: 8px 10px; border: 1px dashed var(--border); border-radius: 8px; }
.px-plan .pxrow { margin-top: 6px; }
.px-plan .px-hint { margin-top: 8px; }
.pxsub { font-size: 12px; color: var(--text-2); flex: 1; }
.px-choose { margin-top: 10px; }
/* 窄屏上标题和正文竖着堆，别把正文挤成一条 */
.px-choose-body { margin-top: 8px; padding: 10px 12px; background: var(--bg-soft); border-radius: 10px; }
.pc-row { display: flex; gap: 10px; font-size: 12px; line-height: 1.7; margin-bottom: 6px; }
.pc-k { flex: 0 0 72px; color: var(--text-2); font-weight: 650; }
.pc-v { flex: 1; color: var(--text-2); }
.pc-note { font-size: 11px; color: var(--text-3); margin-top: 4px; }
@media (max-width: 520px) {
  .pc-row { flex-direction: column; gap: 2px; }
  .pc-k { flex: none; }
}
.proxy-row { margin-bottom: 8px; padding: 10px 12px; background: var(--bg-soft); border-radius: 10px; }
.proxy-row:last-child { margin-bottom: 0; }
/* 一行放不下时按钮整体换行，地址不被挤成省略号 */
.px-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.px-name { font-weight: 600; }
.px-addr { font-size: 12px; color: var(--text-2); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
/* margin-left:auto 把整组按钮推到右端；宽度不够时它整块换到下一行 */
.px-actions { display: flex; align-items: center; gap: 6px; margin-left: auto; flex-wrap: wrap; }
.px-hint { font-size: 11px; color: var(--text-3); margin-top: 6px; }
.px-detail { margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--border); }
.pxrow { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.pxrow:last-child { margin-bottom: 0; }
.pxk { width: 52px; flex-shrink: 0; font-size: 12px; color: var(--text-3); }
.pxv { flex: 1; min-width: 0; }

.ngrp { margin-bottom: 14px; }
.ngrp:last-of-type { margin-bottom: 0; }
.ngrp-head { display: flex; align-items: center; gap: 8px; padding: 0 2px 6px; }
.ngrp-name { font-weight: 650; font-size: 13px; }
.ngrp-meta { font-size: 11px; color: var(--text-3); }
@media (max-width: 900px) { .sub-summary { grid-template-columns: repeat(2, 1fr); } }
@media (max-width: 560px) {
  .sub-summary { grid-template-columns: 1fr; }
  .routing-choice { grid-template-columns: 1fr; gap: 5px; }
  .routing-choice-select { width: 100%; }
  .sub-action-label { width: 100%; flex-basis: 100%; }
  .sub-security-note { margin-left: 0; }
  .sec-caption { display: block; margin: 3px 0 0; }
}
</style>

<!--
  非 scoped：链路胶囊是 nodeCols 的 render 用 h() 造的 vnode，由 n-data-table
  渲染，拿不到本组件 scoped 样式的属性标记。类名统一加 qz- 前缀圈住作用域。
-->
<style>
.qz-topo { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; line-height: 1.9; }
.qz-hop { display: inline-flex; align-items: center; gap: 5px; padding: 1px 7px; border-radius: 6px; font-size: 12px; white-space: nowrap; }
.qz-hop b { font-weight: 600; }
.qz-hop-proto { font-size: 10px; opacity: .75; letter-spacing: .3px; }
/* 入口=蓝、中转=紫、出口=橙、互联网=灰，与管理端链路拓扑的配色一致 */
.qz-hop-entry { background: rgba(32, 128, 240, .12); color: #2080f0; }
.qz-hop-relay { background: rgba(139, 92, 246, .14); color: #7c53d8; }
.qz-hop-egress { background: rgba(217, 119, 6, .14); color: #c2751a; }
.qz-hop-ext { background: rgba(120, 120, 120, .14); color: var(--text-2, #666); }
.qz-hop-inet { background: transparent; color: var(--text-3, #999); padding-left: 0; }
.qz-arrow { font-size: 11px; color: var(--text-3, #999); white-space: nowrap; }
.qz-arrow-warn { color: #d03050; font-weight: 600; }
</style>

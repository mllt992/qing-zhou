# 中转链路计量 P0

本功能将机器网卡、机器代理业务和用户套餐用量分开。它提供每条入口到落地链路的归因，不传递最终用户身份到每一跳；逐用户逐跳属于后续P1。

## 账目与显示

- `server_metrics`及服务商计费规则保持不变，包括IN、OUT、两者之和、取大及人工校准
- `traffic_observations`保存入站用户计数、独立链路、旧共享中转、未知身份和身份冲突。来源合计排除出站诊断计数，避免一份业务在同一机器被加两次
- 正常客户端身份继续使用现有套餐、公共池及免费桶规则。中转身份首先分类，绝不进入套餐解析和扣费路径。中转身份与旧自定义代理名冲突时隔离显示，不把整条链路扣给某个客户
- 同一用户独立使用两台入口仍有两笔消费；一次入口到落地的业务只在客户端入口扣一次
- 流量分析保留旧`attribution`字段，新增`schema_version=2`和`service`。界面显示业务来源、入口付费计量、历史覆盖、采集状态、边界/缺口及发送侧链路诊断。网卡与代理量不要求精确相等
- 中转用户人数不可完整识别或统计存在缺口时，不继续输出可新增用户人数

典型1 GB下载经过两台代理时，每台代理的业务量约1 GB，每台网卡IN＋OUT约2 GB；用户约扣1 GB一次。协议封装、重传、DNS、其他进程和观察窗口都会带来差异，服务商可能只计算一个方向。

## 默认行为与启用

升级后先保留原有线路认证方式；独立链路身份和累计采集两个开关默认关闭。已采集到的旧共享中转和未知身份会进入观测账，不再静默跳过。

管理员在“服务器管理 → 中转链路计量”查看说明并确认后启用。专用接口为：

- `GET /api/admin/relay-metering`
- `PUT /api/admin/relay-metering`，包含`enabled`、`cumulative_enabled`及`confirm=true`
- `POST /api/admin/relay-metering/credentials`，用于显式停用/恢复一个旧共享或旧代凭据，必须再次确认

通用设置接口不能绕过这几个变更入口。不要直接修改数据库中的开关。

独立链路启用需要面板加密密钥。生成的节点间凭据使用保留的随机身份名，密文存储，只由节点配置生成器读取；列表、报表和错误响应不返回凭据或统计身份名。UI确认明确告知凭据创建及节点重启影响。

同机多跳、环路、不支持的落地协议及会提前覆盖中转选择的自定义出口规则会阻止新配置生成，原配置不会因此被改为直连。当前允许的落地渲染包括VLESS、VMess、Trojan、TUIC、Hysteria/Hysteria2、AnyTLS，以及Shadowsocks2022 AES-128/AES-256；mixed可作入口，尚不能作为本管线的落地。每个实际使用的协议和传输组合仍需集成验收，不能将JSON生成成功当成流量验证成功。

## 分阶段下发

稳定边由入口机器、入口入站、逻辑路由、落地机器和落地入站共同确定。端点机器迁移创建新边，旧历史不改写。

1. 规划器先持久化新代身份；入口在目标就绪前拒绝生成切换配置
2. 落地接受新旧身份，并注册统计用户；只有实际配置应用成功后才写入接受状态
3. 入口切换独立outbound，随后标记该代入口配置已应用
4. 当前下发仍通过sing-box配置check、原子文件替换和服务重启，不保证现有连接无中断

目标协议、地址、端口或TLS规格变化会使旧确认失效，并生成新代身份。入口编译和落地确认重新核对目标规格；确认还检查具体入站及认证字段。旧配置不能替新一代背书。代次和历史映射保持持久化。

`prepared`表示待落地接受，`accepted`表示目标已接受但入口尚未确认切换，`active`表示入口配置已应用。它们描述控制面应用结果，**不代表已做完端到端流量测试**。网络、服务启动或配置失败保留在节点同步状态中，需要核查后重新下发。

准备阶段未完成的入口不切换；一般配置安装或服务重启失败仍可能需要节点恢复，不能将它描述为所有失败都自动恢复旧服务。保留兼容凭据是退路，不是零中断部署承诺。

## 旧凭据排空和恢复

默认保留旧共享及旧代凭据，避免尚未迁移的入口立刻失去认证。管理员可查看每个旧凭据的状态和停用阻塞原因。

停用前需要：当前受管入口均完成新身份切换；目标没有待入库批次；旧身份最后正流量之后至少两次完整成功采集。管理员还必须确认所有手工中转也已迁移；系统无法验证配置外的消费者。一次安静采样不足以自动撤销凭据，因此没有自动退役任务。

停用请求先进入`retiring`，实际目标配置确认不再接受该身份后才显示`retired`。恢复旧共享凭据先进入`restoring`，实际认证字段确认正确后才恢复`active`。旧代统计映射永远不因凭据停用而改成普通用户，晚到数据仍只进入观测账。

旧共享凭据已停用时，不能直接关闭独立链路计量。须先恢复并确认目标接受旧共享凭据，再关闭开关。凭据恢复不等于恢复旧协议、旧地址或已删除的入站；涉及拓扑/监听配置变化仍需相应部署恢复。

## 累计采集与可靠性

开启累计模式时使用一次确定的旧计数读出清零并入库交接，此后使用`reset=false`累计快照。单节点采集租约、进程代次和事务游标共同防止重复处理。进程代次由主机boot ID和systemd InvocationID组成，读取前后均验证；PID或配置hash不能代替它。

无法验证进程代次的尚未切换节点保留旧式采集并明确降级；已切换节点若暂时无法验证，则等待恢复，不能退回清零。累计模式一旦生效，普通开关不允许改回清零，以免旧读取器把已入账存量再扣一次。

采集结果先写不可变批次日志，事件、套餐用量、观测记录及游标在事务/保存点内更新。数据库写失败可重试已保存的结果；一个成功身份不会因同批次另一个失败身份而在重试时再收费。重复ID携带不同内容会被拒绝。已落盘批次不允许用稍后读取的数据覆盖。

- 面板重启、短暂SSH中断且核心进程未重启：累计读数恢复后可补差值
- 进程重启、计数下降、倒序样本和不确定的首次清零：保留边界/缺口，不生成负量或伪完整覆盖
- 计划配置重启也记录可能丢失末尾区间的边界
- 没有节点侧持久化计数，不能保证核心崩溃前最后一段流量无损
- 旧reset模式仍存在响应已清零而尚未写入批次日志的窗口，不能声称已经实现无损恰好一次
- 系统只保存采集区间末端时间，不虚构逐秒业务发生时间；跨账期到达的历史差值保留原有采集结束归档语义

## 迁移与历史

版本化迁移`000004_traffic_observation_ledger`增加观测账、批次/游标、日汇总、链路代次、兼容状态与操作审计，不修改已发布迁移。

既有服务器用户样本一次性回填为`historical_user`，不再扣费，也不伪造链路。过去被丢掉的共享relay计数无法恢复。旧用户记录和扩展来源分别显示覆盖起点。

原始观测保留90天；日汇总保留24个月。待重试批次和它的成功事件标记不能清理；完成批次可以清空原始payload，但保留ID/摘要去重墓碑，防止晚到重放再扣费。当前服务器周期分析使用原始保留窗口；长期UTC日汇总可用于后续报表扩展，并非任意时区精确账单。

旧版本无法识别新迁移，不能把二进制降级当成数据库回滚。部署前保留已有升级机制的数据库快照和密钥；恢复旧快照会丢失快照后变更，必须另行评估用量与业务数据。此PR不执行任何生产升级或恢复。

## 验证

常规验证：`go test -race ./...`、`go vet ./...`、`go build ./...`、前端`npm test`及`npm run build`。

实际内核配置：设置`QZ_SINGBOX_TEST_BIN`和`QZ_SINGBOX_REQUIRE_STATS=1`运行sing-box check相关测试。

真实双进程回环测试：`go test -race ./internal/store -run '^TestMeteringRelayRealSingboxTraffic$' -count=1 -v`。它通过本机HTTP代理经生成的VLESS中转访问回环HTTP服务，验证两机器计数、出站辅助计数和唯一入口扣费，不访问生产机器。

CI的`relay-integration`使用固定v0.2.86发布的统计构建及固定SHA256。普通单元测试不要求该二进制；真实流量测试不能因缺少内核能力而冒充通过。某些沙箱禁止sing-box创建netlink接口探测套接字，需在允许该正常只读探测的测试运行器上执行；不要为测试降低生产系统安全配置。

### Ordering and reset boundaries

The collector reserves a durable monotonic sequence before each network read. Wall-clock seconds are only observation timestamps, not ordering keys. Cursors are retained separately per verified process epoch; an old pending epoch can recover its unprocessed delta without replacing the current epoch or recharging the next process. A decrease within the same epoch never lowers its high-water mark: it is marked as an ambiguous reset gap, and traffic below that high-water mark is not claimed as recoverable.

The final destructive reset response and the irreversible cumulative-mode boundary are persisted in one transaction before quota processing. Partial identity failures therefore replay only the saved response; they do not issue another reset. A response lost before durable storage remains an explicit gap, not an exactly-once guarantee.

采样原始日志入库时同时固定来源类别、链路、用户及已能确定的套餐桶；账号级身份先固定用户，再用原有权益优先级选桶，并在任何用量更新前固定选桶结果。失败重试不能按后来被改名、转让或删除的代理名换主人。无法再更新的已删除桶保留待处理状态，不把流量转扣给新用户。

同版本的旧式和累计采集都必须取得同一个节点租约，并在租约内重新读取持久模式。累计切换前必须停用不遵守该租约的旧版本面板或外部 reset 读取器；数据库租约不能约束面板外的程序。

# 中转计量验收清单与证据

本文用于P0链路观测及P1逐用户×机器观测的发布验收。所有执行均在本地、隔离容器或CI中完成，不连接或更改生产服务器。管理员自行部署前，可在自己的隔离环境重复执行。

**状态约定：** `通过`必须对应具体commit、命令、环境和日志；阻塞、跳过及未执行都不等于通过。实现仍有变更时，早先通过不自动覆盖最新版本。[PR #84检查](https://github.com/mllt992/qing-zhou/pull/84/checks)中对应最新commit的原始CI日志是验收证据，发布说明给出最终结论；本文提供可复现清单，不预填未知的最终结果。

## 全协议扩展的验收入口

全协议扩展的范围和具体路径以[协议矩阵](TRAFFIC_PROTOCOL_MATRIX.md)及同commit原始日志为准。下文PR #84、v0.2.87的结果属于早先VLESS/mixed范围，不能作为本次VMess、Trojan、QUIC协议、AnyTLS、SS2022或跨协议路径通过的证据。

新增`TestMeteringRelayRealSingboxProtocolMatrix`分别执行每条路径的`config-check`与`traffic`，两个阶段不能互相替代。普通测试中的`TestRelayProtocol...`只证明状态机、认证匹配或配置逻辑；未设置固定核心二进制而跳过真实测试时，须记为未执行。候选核心仍使用下文同一固定源码构建，不退回旧Vision核心。

## 发布验收清单

按拟发布commit逐项查看原始日志，确认实际执行而非skip：

- Go全量race、vet、build，前端单测及生产build，安装脚本语法
- 按固定官方源码/模块构建候选统计核心，核对完整版本标记与provenance，再执行配置check及P0真实双核心TCP
- P1原有mixed/VLESS回归，及49条全协议/传输路径，同路径双用户TCP/UDP；逐项核对config-check与traffic均实际执行
- P1原有三核心VLESS及三组跨协议三跳，双中转用户与第三用户直连中间跳，TCP/UDP及精确入口扣费
- P1两核心普通TLS＋Vision、WebSocket＋TLS，各自的`config-check`与`traffic`子测试；候选修复核心还需重复TLS/Vision压力，不能用一次偶然通过代替
- 生命周期、实际进程/配置边界，以及可信边界建立后的no-op；P1 Vision相关节点运行核心的修复能力探测，包括旧普通1.14.2、探测失败和过期证据不能放行的用例
- 100/1000用户SQL和配置规模诊断；若有实际核心RSS结果，确认它对应真实启动的核心，而非仅Go分配量

### 已取得的首checkpoint证据（不是最终发布结果）

[PR #84首checkpoint](https://github.com/mllt992/qing-zhou/pull/84)的commit `74b58b7098bb150a771dc9f597794371b70b442b`已有[Linux真实流量CI日志](https://github.com/mllt992/qing-zhou/actions/runs/37244019569/job/111558205307)。发布验收人核对的该次结果为：P0及P1真实流量`-race`测试通过；P1覆盖mixed两机器、VLESS两机器及VLESS三机器的TCP/UDP，用户在各机器独立计数，第三用户仅从中间入口扣套餐。

该次日志中A入口实测332142字节、B入口实测662624字节，三机器场景的C中间入口实测993096字节；各用户套餐增量等于各自入口实测值，每台机器来源上下行与该机器核心原始计数一致。环境为隔离Ubuntu 24.04、固定发布夹具sing-box 1.14.2。这是回环证据，未接触生产，也不是WAN性能测试。

后续源码仍有修改，以上checkpoint不能替代最终commit复测。TLS、Vision及WebSocket当时仅有本地配置check，不包括在这次真实流量通过结论内；应等对应后续checkpoint日志再更新。

首轮云沙箱中的真实内核启动受到netlink套接字`EPERM`限制，正常权限路径及获准的常规升级权限执行均未建立可运行的真实核心环境。这是受阻项，不能把它改写为真实流量通过，也不能为了测试降低生产安全设置。最终真实流量证据以具备正常内核能力的Linux CI或其他获准的隔离测试环境为准。

### Vision失败、协议修复与候选状态

后续checkpoint `83ba025`的真实TLS/Vision CI曾出现RST失败；`a27e1102`未替换核心，只增加trace诊断后曾通过。这类时序变化不能证明问题已经修复，两个checkpoint都不能作为候选修复核心的完整通过证据。

协议级对照已确定：旧sing-vmess `v0.2.8`对1～4字节头部分片的4个用例均失败，官方提交`9b95ab8c9478f8e8ebe5758d325ec8cda8197c5c`对4个用例均通过；额外库级实验的四组组合各有800/800通过记录。这些只支持相应库级测试结论，不代表完成TLS握手、真实核心转发、流量计数或套餐入账。

截至本节记录，尚未取得采用该候选修复核心的完整中转集成通过证据。必须在最新候选commit上构建相同核心，完成重复TLS/Vision压力及全部真实TCP/UDP用例，再由发布说明给出结论。一次不报错、配置check或库级4/4通过均不能提前替代这一门槛。

## 可复现命令

仓库根目录执行常规检查：

```sh
go test -race -timeout=30m ./...
go vet ./...
go build ./...
bash -n internal/assets/install-singbox.sh
bash -n install.sh
(cd frontend && npm test)
(cd frontend && npm run build)
```

依赖、Go/Node版本按仓库CI配置准备；Go构建依赖生成的前端资源。常规测试里真实内核和规模测试默认可以跳过，因此全量单测通过不等于下面的可选测试已运行。

### 固定源码、候选构建与来源校验

真实内核必须带`with_v2ray_api`。当前CI和Release共用`scripts/build-singbox.sh`，不再把旧v0.2.86发布中的普通1.14.2夹具作为Vision修复候选。固定来源为：

- sing-box tag：`v1.14.5`
- sing-box commit：`a6844a780ba173e189a018d0ac62df67cd50b8e9`
- 官方sing-vmess commit：`4acc92a6f6c60e4fad6d73311403a682d70d8b80`（已由官方 1.14.5 的 go.mod 引入，构建不再改写 require）
- sing-vmess伪版本：`v0.2.9-0.20261124075649-4acc92a6f6c6`
- 模块checksum：`h1:vyyQcx1p4dVam3oHPbxIUp/bzP1vKmHaidQ42iuRMa4=`
- go.mod checksum：`h1:P11scgTxMxVVQ8dlM27yNm3Cro40mD0+gHbnqrNGDuY=`
- 完整核心版本：`1.14.5+qz-trojan.b85be9da`
- 固定Go工具链：`go1.25.14`

官方 v1.14.5 的 `go.mod` 仍声明 `go 1.25.5`，没有 `toolchain` 行。上游 CI/Dockerfile 使用 Go 1.27.4 只是他们的构建镜像，不是语言下限；本仓库继续用已审定的 Go 1.25.14 构建节点核心（高于 1.25.5，与 1.14.4 发行构建相同）。官方 v1.14.5 已包含 Vision 与 transport 修复；轻舟只再打 Trojan 维护补丁。构建以 readonly 方式核对 go.mod 中的 sing-vmess 版本与 checksum，避免无关依赖升级。普通版本排序忽略后缀，不可用“1.14.5或更新”推断已审定能力；当前与历史审定标记必须精确匹配，且由实际运行的相关节点核心探测确认。

在隔离Linux amd64环境构建并记录来源：

```sh
bash scripts/build-singbox.sh --output-dir "$PWD/.tmp-metering-core" --arch amd64
cat .tmp-metering-core/sing-box-provenance.json
python3 -c 'import json; d=json.load(open(".tmp-metering-core/sing-box-provenance.json")); [print(b["sha256"]+"  "+b["filename"]) for b in d["binaries"]]' | (cd .tmp-metering-core && sha256sum -c -)
.tmp-metering-core/sing-box-linux-amd64 version
```

脚本也支持`--arch amd64,arm64`；交叉构建成功不等于非本机架构已经执行过真实流量。Release的`sing-box-provenance.json`包含固定源码/模块、工具链、build tags及每个架构产物SHA256；应把产物hash与对应架构条目及发布校验文件逐一核对。版本标记是能力判定条件之一，不能单独替代来源校验和实际运行进程证明。

### 协议级确定性回归

```sh
bash scripts/test-vision-framing.sh --check-baseline
```

该脚本执行Vision padding header在1～4字节处分片、跨`Read`返回的确定性解析回归。`--check-baseline`还要求旧`v0.2.8`的四个指定子测试实际运行并失败；编译或网络错误不能冒充负向对照成功。该脚本没有启动完整核心、没有完成TLS握手，也不测试中转计数或扣费；此前额外的800次库级实验也不是该脚本的固定4子测试，更不能代替下面的完整集成。

### 候选完整核心TCP/UDP集成

使用上面构建并校验的同一候选核心：

```sh
QZ_SINGBOX_TEST_BIN="$PWD/.tmp-metering-core/sing-box-linux-amd64" \
QZ_SINGBOX_REQUIRE_STATS=1 \
QZ_VISION_STRESS_BATCHES=100 \
go test -race ./internal/store -run '^(TestMeteringRelayRealSingbox|TestRelayVLESSFlowMatchesListener)' -count=1 -v
```

重复TLS/Vision路径的隔离压力复测示例：

```sh
QZ_SINGBOX_TEST_BIN="$PWD/.tmp-metering-core/sing-box-linux-amd64" \
QZ_SINGBOX_REQUIRE_STATS=1 \
QZ_VISION_STRESS_BATCHES=100 \
go test -race ./internal/store -run '^TestMeteringRelayRealSingboxSharedUserPath$/^vless$/^2-machines$/^tls-vision$/^traffic$' -count=20 -v
```

发布验收须确认上述过滤器在最终源码中确实匹配、执行了目标子测试，没有`no tests to run`或skip；重复次数、实际连接次数与失败记录应从日志核对。20轮示例只提供可复现的回归负载，不是无故障保证或性能门槛。最终CI采用的压力参数与结论以对应commit原始日志为准。

`TestMeteringRelayRealSingboxTraffic`覆盖P0双核心回环；`TestMeteringRelayRealSingboxSharedUserPath`覆盖P1两/三核心场景。测试使用本机HTTP目标、SOCKS UDP echo和临时数据库；不使用生产地址、TUN或生产系统路由。VLESS场景的本地mixed客户端只是驱动进程，不算被计费机器。真实Vision客户端与服务器都使用同一候选核心。账号/订阅不变不等于所有既有第三方或旧版本客户端已验证；若客户端本身仍包含同一上游分片缺陷，服务端修复不能替它修复。

配置检查和纯逻辑测试也可在安装了同一夹具后运行：

```sh
QZ_SINGBOX_TEST_BIN="$PWD/.tmp-metering-core/sing-box-linux-amd64" \
QZ_SINGBOX_REQUIRE_STATS=1 \
go test ./internal/store ./internal/singbox -count=1 -v
```

此命令包含包内其他测试，需在日志里区分`check`、真实进程执行和skip。当前新增测试将普通TLS＋`xtls-rprx-vision`、WebSocket＋TLS分别拆为`config-check`和`traffic`子测试。测试使用临时证书、匹配的本地信任锚并保持证书验证，不修改系统信任库；WebSocket不启用Vision。具体结果以最终测试源码与执行日志为准，不能从这些测试推断Reality、其他传输或任意TLS组合均已验收。

## 真实流量必须证明什么

### 同路径双用户，两台机器

- 两个原有用户进入同一个入口入站，经过同一物理链路与落地入站，分别使用不同有效负载大小
- 先仅让A产生TCP流量，再仅让B产生TCP流量：A在入口和落地都有自己的计数，B未运行时其计数不变；B运行不改变A计数
- 再执行重复、并发独立TCP连接，验证实际认证路由没有退回整条共享链路
- 分别让A和B做SOCKS UDP ASSOCIATE及UDP echo；校验上下行有效负载，并确认只改变该用户沿途机器的计数
- 分别读取每台核心的实际统计。数据库报表的每个用户上下行必须等于该机器实际计数的已入账增量，不能拿入口计数作为其他机器的期望值
- 每台机器总业务量等于本机用户观测及其他单列来源之和，出站诊断不再累加
- 每位用户套餐总增量精确等于自己的入口实测上下行之和，落地不二次扣费；重放相同批次不再增加用量
- 前后比对客户原UUID、密码、登录名、订阅标识，不因内部计量改变

这里的“精确”针对实际核心计数与账本/套餐一致，不是承诺HTTP应用payload、协议字节和网卡量三者精确相等。

### 三台机器与中途直连

- A、B经过入口 → 中间 → 最终落地，每一跳都独立记录A与B
- C使用自己原有客户端凭据直连中间机器，并继续到最终落地。C在最初入口没有观测，中间机器有直接入口观测，最终落地有中转观测
- C只在中间入口扣套餐一次；A、B只在最初入口各扣一次。C的业务不改变A/B计数
- 在同一台中间机器中同时存在直接用户和转发用户，报表仍按用户合并，入口计费与中转观测保持分离
- TCP与UDP均覆盖以上路径，包含顺序隔离探测和重复连接；不能只用一条已建立的TCP连接代表整个场景

### 部署与采集边界要单独验收

回环夹具会直接构建配置、启动进程并用测试代次入库；它能验证真实流量与计数，但不能替代真实systemd下发、SSH权限路径及boot ID/InvocationID证明测试。

- 配置实际变化后，确认受管核心只按实际变更重启；记录连接中断和采集边界
- 重复无变化规划/编译，配置字节、内部凭据及状态收敛稳定；已验证应用后，相同配置不应再次重启。首次历史统计名分离允许为建立可信进程边界做一次受控重启，并验证之后不重复重启
- 从末端到入口分阶段应用；目标未接受前上游不切换。新增用户、目标规格变化及凭据代次变化不能沿用旧确认
- 确认实际加载的入站认证字段与正确的`auth_user`规则；缺规则、通配规则、错误用户/outbound、提前覆盖、反向或附加限制规则均不能错误标为就绪
- 同机跳转、环路和原生范围外协议被拒绝；拒绝后不静默改直连
- 覆盖账号改名/转让、权益到期、用户删除、链路删除/移动以及晚到旧代样本，旧记录不换归属或转扣其他用户
- 覆盖旧共享凭据停用/恢复状态、待入库阻塞、两次安静采集及手工消费者确认；没有自动撤销旧兼容
- 旧`relay_N` mixed账号：只改变系统统计名，保留客户和旧中转线上凭据；desired JSON或磁盘hash单独不足以证明运行配置。实际受管配置/进程边界未验证时保持缺口，不猜扣
- 非systemd、配置目录/合并配置、非受管进程或无法验证实际配置来源，应明确标为未支持/未证明，不能按成功用例处理

## 账本与可靠性回归

- reset意图在破坏性RPC之前落盘；意图写失败时不调用清零
- 清零响应丢失、读失败、进程变化、日志提交失败均保留可见缺口；保存成功后才原子清除该次不确定标记
- 已落盘而用量处理失败的批次可重试，不再次RPC清零；部分成功用户不会重复扣费
- 累计交接保存最终reset响应及模式边界，已切换节点不能被普通开关或同版本旧读者退回reset
- 同一计数器/代次的较早pending批次冻结区间及归属，较晚批次不能越过；无关用户计数器可独立处理
- 面板重启、重复ID、内容冲突、同秒乱序、进程跨代、同代计数回退及旧代晚到均有断言；不能用PID或秒级时间替代真实顺序/代次
- 报表按一致数据库读快照返回，`users`、`sources`和总量不能因并发写入互相矛盾；读报表不应抢占SQLite写锁
- `observed_user_coverage_complete`与`attribution_ready`分别测试：共享兼容仍存在但本窗口没有未分配流量时，前者可真、后者为假；pending、缺口和无观测时不宣称完整
- 迁移4之后追加5/6/7/8（含逐用户认证HMAC与升级后重新确认）；失败回滚及重启不重复建代次/回填/扣费。用升级前一致数据库备份与原密钥做隔离恢复演练，禁止把新数据库交给旧二进制当作回退

相关测试文件包括`traffic_identity_compat_test.go`、`legacy_relay_identity_test.go`、`relay_metering_users_test.go`、`relay_metering_test.go`、`relay_credential_lifecycle_test.go`、`traffic_ledger_report_test.go`及`internal/sbctl/traffic_snapshot_test.go`。应按最终测试清单核对覆盖，不能从文件名推断未写出的断言。

## 100/1000用户规模、SQL与性能边界

```sh
QZ_METERING_SCALE=1 \
go test ./internal/store -run '^TestMeteringRelayUserScale$' -count=1 -v
```

该测试在隔离SQLite数据库中建立100与1000名用户的单入口/单落地，记录准备时间、配置编译、分配内存、配置字节、outbound/规则数量、无变化重编译、采样入库和报表查询。SQL数来自执行驱动插桩，不含`BEGIN`/`COMMIT`/`ROLLBACK`；不能直接当成网络RPC次数。日志还检查内部用户查找的执行计划，避免逐用户全表扫描。

较早工作版本的1000用户本地合成诊断示例：准备127 ms/4026条SQL，入口编译59 ms/11条SQL，落地确认34 ms/1015条SQL，入口确认54 ms/1015条SQL，无变化处理80 ms/1033条SQL，入库379 ms/11010条SQL，报表11 ms/11条SQL。这不是最终HEAD、性能门槛或生产容量承诺。比较时使用同一测试参数，保存对应commit、环境规格、原始日志和配置规模；后续实现变更可能改变这些数字。

实际核心内存诊断应分别记录100与1000用户配置的实际启动结果、核心版本、RSS取样时点和配置大小。它与配置编译时Go分配内存不同；单点RSS也不是峰值内存。只有最新commit原始CI日志中实际执行的结果，才能支持该版本的相应结论。

评估时明确以下边界：

- 配置与内部用户映射随“用户×经过的物理链路/保留代次”增长；旧逐用户代次仍兼容保留，目前没有对应的retire UI。代次积累会增加配置和核心内存，三跳、多入口及历史代次不是单链路1000用户结果的同等负载
- 配置编译与报表SQL应检查是否出现按用户逐项查表；采样入库当前存在逐用户SQL工作量，11010条不能描述成常数成本或已消除全部N＋1
- 无变化配置必须字节相同；记录no-op也要做多少数据库工作，不能只检查最终JSON
- SQLite写锁时长、并发采集/报表/管理操作、持续积累的观测与保留期，需要另做长期和并发压力验收；一次小数据库合成测试不能代表长期负载
- 真实回环日志可记录完成的TCP/UDP应用字节、延迟及应用吞吐，并记录各核心单点RSS；回环吞吐不是WAN吞吐，单点RSS不是峰值内存或VPS选型建议
- 100/1000测试不建立1000个真实并发代理连接，也不覆盖实际VPS CPU、带宽、丢包、TLS握手成本及公网距离

## 发布验收收口

1. 固定最终commit，重新跑受最后改动影响的检查；记录同一commit的CI链接及真实流量日志
2. 将结果明确分为通过、失败、阻塞、跳过/未执行，逐项列出TLS/传输等未验收组合
3. 核对迁移与回退说明、默认关闭但账本逻辑有变化的说明、重启断连及历史不可修复边界；明确管理员须自行重装相关核心并重新验证运行能力，Docker镜像仅含面板/probe
4. 发布制品与验收commit对应，核心使用相同固定构建脚本并附带`sing-box-provenance.json`；核对架构hash及来源，测试中的临时数据库、测试密钥和计数器不得进入发布制品
5. 发布与部署分开。发布不意味着已经接触、升级或验证用户生产服务器；管理员选择何时在自己的环境测试和部署

# 固定核心的 Trojan 分段首包与握手清理维护补丁（#87）

## 性质与来源

**这是轻舟项目维护补丁，不是上游官方修复。** 截至编写时，上游 sing-box `testing`/`main` 的 `transport/trojan/service.go` 仍是单次 `conn.Read(key[:])`，没有可回补的官方提交。

- 基线：官方 sing-box v1.14.2（`af6e64c3b69e6132ebaee0e1a3d24e93903f6709`），保留官方 sing-vmess `9b95ab8c9478` Vision 修复和已审的 07512b10 WS/HTTPUpgrade 三文件回补（见 [CORE_TRANSPORT_BUFFER_FIX.md](CORE_TRANSPORT_BUFFER_FIX.md)）。不升级 sing-box、alpha 或其他依赖。
- 补丁：`scripts/trojan-handshake/qz-trojan-handshake.patch`，只改 `transport/trojan/service.go`，与 transport 回补文件不重叠，在其后应用。
- 补丁 SHA-256：`b85be9dae59431e59e431cdff004629ea43c869983f136e6fccf64cec50d48a7`
- 文件摘要（`scripts/trojan-handshake/manifest.json`）：
  - 应用前 `6ffe5d2408d4b391b2e625de0777debecbd02c1a01c1b079f46c18fcac114edf`
  - 应用后 `8fe277d78faccec858f9460ca24793ca9ee132bdcb43738eb27147095d7d5bb3`
- 完整构建标记：`1.14.2+qz-vmess.9b95ab8c9478-transport.07512b10-trojan.b85be9da`（`scripts/singbox-pins.sh` 的 `CORE_VERSION`）
- 来源说明：补丁由本仓库针对 #87 编写；manifest 中 `kind=qing-zhou maintenance patch`、`upstream_fix=false`，`apply-trojan-handshake-patch.sh` 会拒绝任何把它标为上游修复的 manifest。

`apply-trojan-handshake-patch.sh` 校验基线 commit、补丁 hash、唯一文件集合及应用前后文件 hash，异常即中止。CI 与 Release 都调用同一个 `build-singbox.sh`，产物 `sing-box-provenance.json` 升到 schema 3，新增 `trojan_handshake_patch`（完整 manifest），并保留基础源码、Vision 模块、transport 补丁、工具链与 amd64/arm64 二进制 hash。

## 根因

合并 PR #86 后 main CI 的真实 Trojan + HTTPUpgrade 并发流量出现 502，落地日志 `inbound/trojan: process connection ...: bad request size: fallback disabled`。

原实现（`transport/trojan/service.go` `NewConnection`）只调用一次 `conn.Read(key[:])`，读到少于 56 字节就直接判为 "bad request size" 并转 fallback（未配置 fallback 时即报错断开）。流式传输不保证一次读满：07512b10 回补后 HTTPUpgrade 服务端把 Hijack 预读字节放进 `CachedConn`，首次 Read 只返回缓存部分；普通 TCP/TLS 分段、WS 帧、gRPC/HTTP2 消息边界也可能把 56 字节密钥拆开。回补前预读字节直接丢失，问题被另一种错误掩盖；回补后才显现为偶发 502。

## 补丁行为

1. **按协议边界累积**：密钥读满 56 字节后才查用户；命令、地址、结束 CRLF 用完整读取。只读到请求头末尾，后续业务数据留在连接里，首包语义不变。
2. **只用公开格式提前判断**：Trojan 密钥是小写十六进制。任何一个非 `[0-9a-f]` 字节立即判为非协议流量并转 fallback（HTTP、TLS ClientHello 等首字节即可判定）。**部分密钥从不与已配置用户比较**，不能逐字节探测密钥，认证规则未放宽。
3. **区分结果**：首字节前 EOF 原样返回；部分字节后 EOF → fallback（"bad request size: unexpected EOF"）；格式错误 → fallback（"bad request format"）；完整但未知密钥 → fallback（"bad request"，与原来一致）；连续 100 次 0 字节无错误 → `io.ErrNoProgress`；认证后命令/地址错误仍是错误，不转 fallback。
4. **有界握手**：从第一次读取前开始计时，密钥、CRLF、命令、地址、CRLF 共用一个预算（`C.TCPTimeout` = 15 秒）。慢速到达不会延长总预算；更早的上下文取消/期限先生效；已存在的连接期限从不被延长或清除。读取在独立 goroutine 进行，主流程等待结果、超时或 ctx 结束。
5. **只中断当前连接/流**：超时或取消时，支持期限的连接（TCP、TLS、HTTPUpgrade、QUIC 流）设置立即读期限；不支持的（WebSocket、HTTP/2、gRPC lite）调用各自的单流 `Close`，不碰共享会话。WebSocket 关闭帧写入受该传输自身写期限约束。随后最多等 1 秒让读取退出；官方 gRPC 服务端流在 handler 返回前无法中断，由调用方必定执行的错误清理（`CloseOnHandshakeFailure` → onClose → 流结束）释放。
6. **成功或 fallback 前同步结束**：只有拿到读取结果后才继续，任何中断都发生在返回错误之前，不会有迟到的定时器关闭已交给 handler 的正常长连接。
7. **fallback**：已消耗字节通过 `CachedConn` 完整、按序、只回放一次。不自动重放应用请求，不用重试掩盖握手失败。

### 兼容性变化（明确记录）

- 原来首读不足 56 字节且全是十六进制时立即 fallback；现在会继续等待到 56 字节、EOF 或握手预算结束。握手预算耗尽时返回错误并关闭，不再转 fallback。
- 原来首次读取同时返回数据和 EOF 时直接报错；现在部分密钥 + EOF 会把已读字节交给 fallback。
- 原来没有握手期限，静默客户端可无限占用连接；现在 15 秒后关闭。

## 确定性回归

`scripts/test-trojan-handshake.sh`（要求 Go 1.25.14；CI 与 Release 现有的 `test-transport-buffer.sh --check-baseline` 步骤末尾会串联执行它，参数相同）：

- `--check-baseline`：只复制仅用公开 API 的 `fragment_test.go.txt` 到**未打补丁**的基线，必须看到 36 个分段/预读负控实际运行并失败（1 字节逐字节、首段 1/10/55 字节、7 字节分片；预读 1/4/11/55 字节；TCP/UDP × fallback 开/关），20 个完整首密钥正对照和 fallback 兼容组必须通过。编译失败、`no tests to run` 不算复现。
- 打上 transport + Trojan 补丁后，以 `-race`、`with_grpc,with_quic` 运行全部 13 组（默认 20 轮），不允许 skip 或部分通过：
  - 分段、预读缓冲、TCP/UDP、fallback 开/关；fallback 对 HTTP、TLS 类、长文本、错误认证、十六进制前缀+EOF 的完整唯一回放
  - EOF、未知命令、截断地址、无进度、预先取消、更早连接期限、更早 ctx 期限、无首包、慢速输入（按字节持续到达仍在预算处结束）、预算内慢速成功
  - 握手预算后正常长连接持续收发，handler 无迟到中断
  - 64 路随机分片环回并发，goroutine 回收检查
  - 真实 sing-box TLS + WS / HTTPUpgrade / HTTP2 / gRPC lite / 官方 gRPC / QUIC：分段握手 + 预算后长连接；卡住的握手在预算内结束、调用方关闭有界、客户端看到流关闭、阻塞读取在清理后归零；HTTP2/gRPC/QUIC 同会话的已建立兄弟流在清理后继续收发，并能新开流
- 这些是库级测试，不能代替完整核心实流。

## 实流与能力门禁

- 实流步骤中每条 Trojan 路径按 `QZ_TROJAN_STRESS_BATCHES`（未设置时沿用 CI/Release 已设的 `QZ_VISION_STRESS_BATCHES=100`）执行：100 批 × 8 个独立并发请求，每批收集并报告全部失败，不重试。原 49 协议 + 3 WS 矩阵、跨协议三跳、逐机器 raw=报表、入口单次扣费、Vision 100 批、8 组 100/1000 用户诊断均不缩减。
- 新的独立能力 `has_trojan_handshake_fix` 只认精确标记 `…-trojan.b85be9da`。旧 Vision / transport 标记、带后缀版本、更高 semver、伪造的布尔标志都不满足。新标记同时满足 Vision 与 transport 能力。
- P1 逐用户机器观测中，凡**终结 Trojan 入站**的机器（任何传输）需要此能力；只运行 Trojan 客户端出站的上游不需要。预检、启用校验、下发前后实际运行核心校验及流量归属就绪（`trojan_core_unverified`）都按此独立判断。
- `http-tls` 传输在并发压力下有一个既有的 x/net HTTP/2 少计竞态，vless/vmess 同样存在，不属于本补丁。测试只做有界、按日志对证的放宽，见 [TRAFFIC_PROTOCOL_MATRIX.md](TRAFFIC_PROTOCOL_MATRIX.md) 的“已知限制”。
- 计量默认关闭不变；不轮换原账户凭据；历史未知流量不猜分；不修改生产数据库，不自动部署或重启生产节点。合并不等于发布。

## 边界

- arm64 只做构建与 provenance，未执行真实网络验收。
- 每连接有界只保证单条握手不会无限占用，不是公网容量保证。
- 修复受管核心不能修复客户设备上的第三方客户端或第三方服务端。

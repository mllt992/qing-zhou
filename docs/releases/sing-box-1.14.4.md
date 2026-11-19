# 节点内核 1.14.2 → 1.14.4

本次将发行构建锁定为官方 sing-box v1.14.4（提交 `c005f35300ce76427dc285481ec5695f5b179cd2`），保留全部现有构建标签，尤其是 `with_v2ray_api` 与 `with_grpc`。amd64 / arm64 发行资产仍由既有 release 流程生成；合并本改动不会发布 release，也不会自动升级现有节点。

官方 v1.14.3 已包含此前轻舟回补的 Vision 分片修复与 WS/HTTPUpgrade 读缓冲修复；v1.14.4 使用更新的官方 sing-vmess（`a0c58542bd9d`，含 Vision close-after-direct-write 修复），并修正 HTTPUpgrade 客户端 IPv6 Host 方括号。发行构建因此不再应用 `scripts/transport-buffer` 三文件补丁，改为核对官方 go.mod 已引入审定的 sing-vmess 版本。

轻舟 Trojan 分段握手维护补丁（#87，`b85be9da…`）仍然需要：截至 v1.14.4，上游 `transport/trojan/service.go` 未变。该补丁在干净的官方 1.14.4 树上应用。当前完整标识为 `1.14.4+qz-trojan.b85be9da`。已安装的历史审定 1.14.2 标记（Vision / transport / Trojan）在能力门禁中仍然有效。

安装脚本的官方应急回退也锁定 v1.14.4，不随上游 latest 漂移。官方包仍不含 `with_v2ray_api`，也不含 Trojan 维护补丁，不能提供本面板的逐用户流量计量/配额执行；脚本会明确警告，应重新安装面板托管版本。

## 兼容性检查

已对照 [上游 v1.14.2…v1.14.4](https://github.com/SagerNet/sing-box/compare/v1.14.2...v1.14.4)。主要为既有修复回合、依赖与传输边角修正。面板生成配置未引入 1.15 alpha 字段，也无需为本次升级改写 JSON 选项。`domain_strategy` 等已弃用字段仍由生成路径剥离。

Release 仍以刚构建的二进制执行 `TestGeneratedServerConfigPassesSingboxCheck`，保留生成配置中的 `experimental.v2ray_api`；缺失流量插件或配置校验失败会阻止上传资产。

来源：[1.14.4 release](https://github.com/SagerNet/sing-box/releases/tag/v1.14.4)、[完整 compare](https://github.com/SagerNet/sing-box/compare/v1.14.2...v1.14.4)。

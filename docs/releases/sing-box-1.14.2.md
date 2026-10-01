# 节点内核 1.14.0 → 1.14.2

本次将发行构建锁定为 sing-box v1.14.2，保留全部现有构建标签，尤其是 `with_v2ray_api` 与 `with_grpc`。amd64 / arm64 发行资产仍由既有 release 流程生成；合并本改动不会发布 release，也不会自动升级现有节点。

安装脚本的官方应急回退也锁定 1.14.2，不随上游 latest 漂移。官方包仍不含 `with_v2ray_api`，不能提供本面板的逐用户流量计量/配额执行；脚本会明确警告，应重新安装面板托管版本。

## 兼容性检查

已检查 [上游 1.14.0…1.14.2 的 77 个提交](https://github.com/SagerNet/sing-box/compare/v1.14.0...v1.14.2)。主要为 DNS、网络重置、UDP 目标、gRPC/HTTP 半关闭与并发、流量计数、平台相关修复。配置 option 变更为 omitempty 修正、内部辅助方法和不序列化字段移除，没有发现本面板生成配置需要迁移的 JSON 字段变化。未引入 1.15 alpha 字段。

Release 现在以刚构建的 1.14.2 二进制执行 `TestGeneratedServerConfigPassesSingboxCheck`，保留生成配置中的 `experimental.v2ray_api`；缺失流量插件或配置校验失败会阻止上传资产。对官方无插件包运行本地兼容性测试时仍可裁掉 experimental 区块，但不接受它通过发行验证。

来源：[1.14.2 release](https://github.com/SagerNet/sing-box/releases/tag/v1.14.2)、[完整 compare](https://github.com/SagerNet/sing-box/compare/v1.14.0...v1.14.2)。

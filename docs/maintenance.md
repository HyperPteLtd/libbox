# 维护指南

## 核心版本

当前固定为 sing-box **v1.14.4**。升级时显式选择正式发布的 `vX.Y.Z` 标签，并对齐该发布版的核心依赖集；不独立追踪子模块的最新提交。上游发布版自带的固定 pseudo-version 依赖可以保留。

更新 [go.mod](../go.mod)、[go.sum](../go.sum)、[内部版本元数据](../internal/singbox/runtime.go)、README 和 NOTICE。适配代码集中在 `internal/singbox`，公开 API 保持独立于上游类型。版本检查会拒绝 sing-box 的预发布/提交伪版本、模块替换及元数据不一致。

升级后核对配置格式、宿主接口和依赖解析结果。Go 的版本选择可能保留既有较高版本，因此只修改 sing-box 一项依赖并不等于对齐发布依赖集。

SDK 自带的 `loadbalance` 出站组（`internal/singbox/loadbalance`）通过 `internal/singbox/registry.go` 注册到上游出站注册表，其余注册表与 `include.Context` 保持一致。升级时还要核对：

- `include` 各注册表函数和 `box.Context` 的签名；上游是否自行注册了 `loadbalance` 类型（注册表测试会报错）。
- 路由对组的解析：v1.14.4 通过 `Selected` 找到链末端出站后直接拨号，因此组的 `Selected` 返回按站点哈希的内部出站，展示用 `Current`。
- 复用的接口：`group.URLTestOutbounds`、`group.RealTag`、`urltest.HistoryStorage`、`interrupt.Group`、`pause.RegisterTicker`、`adapter.OutboundGroup` / `URLTestGroup` / `Referrer` / `InterfaceUpdateListener` 及 `adapter.ContextFrom` 中的目标元数据。

## 协议生成与兼容

消息定义为 [control.proto](../proto/libbox/v1/control.proto)。兼容扩展新增字段或方法，字段编号不复用；删除字段时保留 `reserved`。不兼容变化需要升级协议主版本。

工具版本与下载校验值位于 [toolchain.json](../proto/toolchain.json)。Go/Java/C#/Python/Swift 绑定随源码提交；修改 schema 后通过工具重新生成。

```bash
bash scripts/install-swift-protobuf.sh
python3 scripts/generate-protocol.py --swift
python3 scripts/generate-protocol.py --swift --check
```

不处理 Swift 时省略 `--swift`。公开 Go/gomobile 接口按 [api-v1.txt](../testdata/api-v1.txt) 核对，C 接口按 [libbox.h](../include/libbox.h) 核对。

## 检查命令

```bash
make test race vet protocol-check
make android-host-tests    # JDK 17
make windows-host-tests    # .NET 8+
make apple-host-tests      # macOS + Swift
```

Go 测试位于 `internal/` 和 `protocol/`；宿主逻辑测试位于示例目录。CI 配置见 [validate.yml](../.github/workflows/validate.yml)。

原生传输冒烟先构建当前平台动态库，再使用安装了 [Python 依赖](../scripts/requirements-smoke.txt) 的环境运行：

```bash
make native
python3 scripts/smoke-protobuf.py dist/darwin-arm64/libbox.dylib
```

将示例库路径替换为实际产物路径。该测试不创建真实 TUN。平台构建方法见 [平台接入](hosts.md)；签名、VPN、DNS、网络切换和后台行为需要在目标系统单独验收。

## 清理

`make clean` 删除本仓库的 `dist`、`_build`、平台构建目录、生成的 Xcode 项目、`tools/bin` 和 Python 缓存。保留源码、protobuf 生成代码及全局工具链；再次构建移动端 SDK 前需重新运行 `make mobile-tools`。

Go 模块缓存独立于项目产物。需要清空时执行 `go clean -modcache`，它会影响共享该缓存的其他 Go 项目，后续构建将重新下载锁定的依赖。

# libbox

libbox 是用于 Android、Windows、macOS 和 iOS 的 sing-box 嵌入 SDK，提供配置校验、启停、重载、代理组控制、测速、运行状态和事件读取，并在核心上注册了按站点分配节点的 [`loadbalance` 出站组](docs/api.md#loadbalance-出站组)。

核心固定为 **sing-box v1.15.0-alpha.13（预发布版）**，依赖由 [go.mod](go.mod) 和 [go.sum](go.sum) 锁定。升级时显式选择发布标签，不跟踪分支或最新提交。

## 接入方式

宿主通过 protobuf `Request` / `Response` 调用 SDK，通过 `HostRequest` / `HostResponse` 提供 TUN 创建、socket 保护等系统能力。协议定义见 [control.proto](proto/libbox/v1/control.proto)。

Go/gomobile 使用 `MessageSession`，C/.NET 使用动态库中的会话函数。典型调用顺序：

```text
创建消息会话 → create_engine → check_config → start
运行期间：查询状态、选择节点、测速、读取事件
结束：stop 或 destroy_engine → 释放消息会话
```

`Config.config_json` 使用 sing-box v1.15.0-alpha.13 的原生 JSON 配置。TUN 数据直接通过系统接口传输。

| 平台 | SDK 产物 | 宿主示例 | TUN 所有权 |
|---|---|---|---|
| Android API 24+ | AAR | [VpnService](examples/android) | 宿主持有原 FD，SDK 使用副本 |
| Windows x64 | DLL + C 头文件 | [.NET](examples/windows) | 核心管理 Wintun |
| macOS 13+ | XCFramework / 动态库 | [App + System Extension](examples/apple) | NetworkExtension 持有原 FD，SDK 使用副本 |
| iOS 15+ | XCFramework | [App + Packet Tunnel Extension](examples/apple) | NetworkExtension 持有原 FD，SDK 使用副本 |
| Linux 路由器（OpenWrt 等） | 命令行 `sing-box`（静态链接） | 见下文 | 核心自行创建 TUN |

## 路由器命令行

`cmd/sing-box` 是固定版本 sing-box 自带的命令行程序，换成 SDK 的注册表，配置里可以使用 `loadbalance` 出站组。`run`、`check`、`format`、`merge`、`version` 的参数和输出与上游一致，可以直接替换路由器上的 `sing-box`（例如 OpenWrt passwall 按 `sing-box version` 的第一行和 `Tags:` 行判断版本与功能）。`version` 另外输出一行 `libbox: <SDK 版本>`。

每个 [Release](https://github.com/HyperPteLtd/libbox/releases) 附带各 CPU 的静态程序 `libbox-<版本>-linux-<目标>.tar.gz` 和 `sha256sums`：

| 目标 | 常见 OpenWrt 架构 |
|---|---|
| `amd64` | `x86_64` |
| `arm64` | `aarch64_cortex-a53`、`aarch64_generic` 等 |
| `armv7` / `armv6` / `armv5` | `arm_cortex-a7_neon-vfpv4` 等 / `arm_arm1176jzf-s_vfp` / `arm_arm926ej-s` 等 |
| `mipsle-softfloat` / `mips-softfloat` | `mipsel_24kc` 等 / `mips_24kc` 等 |
| `386-softfloat`、`riscv64`、`loong64` | `i386_*`、`riscv64_generic`、`loongarch64_generic` |

本地构建（需要 Linux 和 GNU tar，macOS 上可用 Linux 容器），产物在 `dist/cli/`，相同源码得到相同的压缩包：

```bash
make cli-test
make cli                         # 全部目标
make cli CLI_TARGETS="arm64"     # 只构建部分目标
```

编译标签为 `with_quic,with_wireguard,with_utls,with_clash_api,badlinkname,tfogo_checklinkname0`，不含需要 cgo 的功能（naive 出站等），也不含 gvisor：路由器的 TUN 用默认的 Go 栈或 system 栈，WireGuard 也不需要它，去掉后 arm64 程序小约 3.9 MB。需要 mixed / gvisor 栈时用 `make cli CLI_TAGS=...,with_gvisor,...` 自行构建。

## 构建

需要 Go 1.25.5 或更高版本。当前平台的 C 动态库还需要 C 编译器：

```bash
make native
```

产物为 `dist/<os>-<arch>/libbox.{dll,dylib,so}` 和 `libbox.h`。Android、Apple 和 Windows 交叉构建的工具链与命令见 [平台接入](docs/hosts.md)。

默认构建标签为 `with_quic,with_wireguard,with_utls,with_clash_api`。Apple 移动端额外启用 `with_low_memory`。TUN 默认使用新的 Go TCP/IP 栈，建议省略 `stack`；兼容使用 mixed/gvisor 时需添加 `with_gvisor`。

```bash
make test race vet          # 内部模块、协议测试及静态检查
make clean                  # 清理本仓库产物与工具缓存
```

## 文档

- [API](docs/api.md)：会话、protobuf 方法、宿主回调和 C ABI。
- [平台接入](docs/hosts.md)：构建产物、宿主集成、FD 所有权及 Apple 内存配置。
- [维护指南](docs/maintenance.md)：协议生成、核心升级、检查命令和清理范围。

GPL-3.0-or-later，详见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。

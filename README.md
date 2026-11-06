# libbox

libbox 是用于 Android、Windows、macOS 和 iOS 的 sing-box 嵌入 SDK，提供配置校验、启停、重载、代理组控制、测速、运行状态和事件读取，并在核心上注册了按站点分配节点的 [`loadbalance` 出站组](docs/api.md#loadbalance-出站组)。

核心固定为 **sing-box v1.14.4**，依赖由 [go.mod](go.mod) 和 [go.sum](go.sum) 锁定。升级时显式选择正式发布版本。

## 接入方式

宿主通过 protobuf `Request` / `Response` 调用 SDK，通过 `HostRequest` / `HostResponse` 提供 TUN 创建、socket 保护等系统能力。协议定义见 [control.proto](proto/libbox/v1/control.proto)。

Go/gomobile 使用 `MessageSession`，C/.NET 使用动态库中的会话函数。典型调用顺序：

```text
创建消息会话 → create_engine → check_config → start
运行期间：查询状态、选择节点、测速、读取事件
结束：stop 或 destroy_engine → 释放消息会话
```

`Config.config_json` 使用 sing-box v1.14.4 的原生 JSON 配置。TUN 数据直接通过系统接口传输。

| 平台 | SDK 产物 | 宿主示例 | TUN 所有权 |
|---|---|---|---|
| Android API 24+ | AAR | [VpnService](examples/android) | 宿主持有原 FD，SDK 使用副本 |
| Windows x64 | DLL + C 头文件 | [.NET](examples/windows) | 核心管理 Wintun |
| macOS 13+ | XCFramework / 动态库 | [App + System Extension](examples/apple) | NetworkExtension 持有原 FD，SDK 使用副本 |
| iOS 15+ | XCFramework | [App + Packet Tunnel Extension](examples/apple) | NetworkExtension 持有原 FD，SDK 使用副本 |

## 构建

需要 Go 1.25.5 或更高版本。当前平台的 C 动态库还需要 C 编译器：

```bash
make native
```

产物为 `dist/<os>-<arch>/libbox.{dll,dylib,so}` 和 `libbox.h`。Android、Apple 和 Windows 交叉构建的工具链与命令见 [平台接入](docs/hosts.md)。

默认构建标签为 `with_quic,with_wireguard,with_utls,with_clash_api`。Apple 移动端额外启用 `with_low_memory`；使用 mixed/gvisor TUN 栈需添加 `with_gvisor`。

```bash
make test race vet          # 内部模块、协议测试及静态检查
make clean                  # 清理本仓库产物与工具缓存
```

## 文档

- [API](docs/api.md)：会话、protobuf 方法、宿主回调和 C ABI。
- [平台接入](docs/hosts.md)：构建产物、宿主集成、FD 所有权及 Apple 内存配置。
- [维护指南](docs/maintenance.md)：协议生成、核心升级、检查命令和清理范围。

GPL-3.0-or-later，详见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。

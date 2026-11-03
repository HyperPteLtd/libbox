# libbox

独立的 sing-box 嵌入 SDK。Android、Windows、macOS、iOS 使用同一套 **protobuf 控制消息与宿主消息**。上游变化集中在 `internal/singbox`；不依赖上游 `experimental/libbox`，也不保留旧 JSON 控制接口。

```text
Android VpnService / Windows DLL host / Apple shared raw-TUN NetworkExtension
                               ↓ Request / Response
                         MessageSession
                               ↓
                    生命周期与内部数据结构
                               ↓
                      internal/singbox
                               ↓
                    固定提交的 sing-box

核心 → HostRequest / HostResponse → 宿主系统 API
```

配置文件仍是原生 sing-box JSON，通过 protobuf 的 `Config.config_json` 作为内容传递。TUN 数据直接走系统接口，不通过控制协议转发。

## 功能

- 创建/销毁实例、配置校验、启停、重载失败恢复。
- 状态、流量、代理组、节点选择、测速、模式、暂停和连接关闭。
- 有界事件队列、单条日志 16 KiB 上限、请求大小限制和截止时间。
- 一种宿主回调：`HostMessageHandler`。TUN 创建、socket 保护、连接归属都使用 protobuf。
- 可选 NetworkExtension 内存 profile、进程级多实例协调、内存压力通知。
- Android AAR、Apple XCFramework、Windows x64 DLL、macOS/Linux C 库构建入口。
- 四端最小宿主示例、协议生成与兼容检查、真实本地代理测试和 C ABI 冒烟测试。

## 单一接口

Go / gomobile 公开入口：

```go
type HostMessageHandler interface {
    HandleHostMessage(request []byte) ([]byte, error)
}

session := libbox.NewMessageSession(hostHandler) // native 模式可传 nil
defer session.Close()                           // 原生资源清理
responseBytes := session.HandleMessage(requestBytes)
```

先发送 `create_engine`，再发送 `start` 等请求；结束时发送 `destroy_engine`。
宿主对象构造与释放只是语言绑定的资源管理，所有应用操作都在 protobuf 消息中。
协议定义见 [control.proto](proto/libbox/v1/control.proto)，调用规则见 [API 文档](docs/api.md)。

DLL 仅导出：

```c
libbox_session_new
libbox_session_call
libbox_free_buffer
libbox_session_free
```

详见 [C ABI](docs/c-abi.md)。

## 平台接入

| 平台 | 接入与最小示例 | TUN 所有权 |
|---|---|---|
| Android API 24+ | [Java Activity + VpnService](examples/android)；AAR 含 arm64-v8a、armeabi-v7a、x86_64 | VpnService 持有原 FD，SDK 使用副本 |
| Windows x64 | [.NET protobuf/DLL 宿主](examples/windows) | native 模式，由核心管理 Wintun |
| macOS 13+ | [SwiftUI App + Network System Extension](examples/apple) | 与 iOS 共用 raw TUN FD Provider；NE 持有原 FD |
| iOS 15+ | [SwiftUI App + PacketTunnelProvider](examples/apple) | 与 macOS 共用 Provider；SDK 只持有 FD 副本 |

构建、签名要求及验证范围见 [宿主说明](docs/hosts.md)。本轮 Apple 统一工作仅进行源码检查、
模拟 FD/生命周期单元测试和未签名编译，不运行真机、模拟器或系统 VPN。

## 构建

核心固定到 `72b3c1cc2cb178b7a9120d4c64c61fcad6b1fd46`：
`github.com/sagernet/sing-box v1.14.5-0.20261103042725-72b3c1cc2cb1`。
独立检出即可构建，无相邻源码或本地 replace 依赖。

```bash
make test
make race
make vet

make native                 # 当前平台 C 库
make windows                # Windows x64 DLL，需要 MinGW-w64
make mobile-tools           # 固定 gomobile v0.1.13，安装到 tools/bin
make android
make apple

python3 scripts/generate-protocol.py --check
```

默认核心标签：`with_quic,with_wireguard,with_utls,with_clash_api`。
Apple 移动端额外使用 `with_low_memory`。可选协议通过 `GO_TAGS` 控制；
未编译的协议在配置阶段报错。版本与能力通过 `get_info` 查询。

产物在 `dist/`，平台示例的构建命令见 [宿主说明](docs/hosts.md)。
`make clean` 清理本仓库构建产物和工具缓存；保留 protobuf 生成源码。

## 协议与升级

protobuf 字段编号不复用；兼容扩展添加字段或方法，不扩充原生桥接接口。
公开 Go/C 接口、请求字段编号和线格式均有检查。SDK 协议版本与核心版本分开，
原生核心配置格式仍随所固定核心演进。详见 [协议设计](docs/protocol.md) 和 [升级流程](docs/upgrading.md)。

设计参考了 libclash，以及用户提供的 `singbox-desktop/src/core`：后者实际使用 mihomo，
本项目借鉴其核心/分发/传输分层、protobuf 和 Apple FD 生命周期，未移植其产品配置编排、
服务用户所有权或 UI 逻辑。

Apple 两端还参考 Hyper mobile / desktop 的 raw TUN FD 设计，共用 `examples/apple/Shared`，
仅部署入口和必要的系统策略保留平台区别。

GPL-3.0-or-later，见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。

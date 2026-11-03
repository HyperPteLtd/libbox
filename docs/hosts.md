# 平台接入

所有宿主使用同一份 [protobuf 协议](../proto/libbox/v1/control.proto)。示例默认使用 direct 出站，完整控制接口见 [API](api.md)。

## 网络模式

| 平台 | VPN 模式 | 宿主职责 |
|---|---|---|
| Windows | NATIVE | 提供运行权限；核心管理 Wintun、地址和路由 |
| Android | HOST | VpnService 建立 TUN，提供网络快照和 socket 保护 |
| macOS / iOS | HOST | NetworkExtension 管理设置与原 utun FD，SDK 管理 FD 副本 |

NATIVE 也可用于普通代理配置。Apple 的 TUN 配置必须使用 HOST；每个 NetworkExtension 配置恰好一个 TUN。

HOST 模式由宿主提供物理网络快照，不接受显式的 `bind_interface`、`inet4_bind_address`、`inet6_bind_address`、`default_interface`、`protect_path`、`netns` 或 `auto_redirect`。

v1.14.4 在默认构建标签下使用 system TUN 栈。mixed/gvisor 需要 `with_gvisor`；Apple `include_all_networks` 必须使用 gvisor。可按需构建：

```bash
make apple GO_TAGS=with_quic,with_wireguard,with_utls,with_clash_api,with_gvisor
```

构建前需按下文安装对应工具链。默认标签定义在 [Makefile](../Makefile)。

## Windows x64

需要 Go 1.25.5+ 和 MinGW-w64。Windows 本机构建默认使用 `gcc`，macOS 交叉编译默认使用 `x86_64-w64-mingw32-gcc`；可通过 `WINDOWS_CC` 指定编译器。.NET 示例需要 .NET 8+。

```bash
make windows
dotnet publish examples/windows/LibboxHost.csproj -c Release -r win-x64 --self-contained false -o dist/examples/windows
```

SDK 输出到 `dist/windows-amd64/`。示例从可执行文件目录加载 `libbox.dll`，也可通过 `LIBBOX_NATIVE_PATH` 指定绝对路径。DLL 保持加载至进程退出。

| 启动参数 | 用途 |
|---|---|
| `--smoke` | 检查 protobuf 和核心生命周期，不创建 TUN |
| `--tun-smoke` | 创建临时命名的 Wintun，不设置默认路由，然后停止 |
| `--tun` | 运行 VPN 示例，Ctrl+C 停止 |
| 配置文件路径 | 使用自定义 sing-box JSON 配置 |

TUN 模式需要管理员权限；Wintun 二进制随核心依赖提供。示例代码位于 [examples/windows](../examples/windows)。

## Android

需要 Go 1.25.5+、JDK 17、Android SDK 和 NDK。设置对应的 `JAVA_HOME`、`ANDROID_HOME`、`ANDROID_NDK_HOME`。示例 compile/target SDK 为 35，最低 API 24。

```bash
make mobile-tools
make android
```

输出 `dist/mobile/libbox.aar`，包含 arm64-v8a、armeabi-v7a 和 x86_64。构建 APK 可运行以下命令，它会先构建 AAR，再使用脚本固定的 Gradle：

```bash
python3 scripts/build-android-example.py
```

APK 位于 `examples/android/app/build/outputs/apk/debug/app-debug.apk`。示例代码位于 [examples/android](../examples/android)。

Activity 请求 VPN 授权后启动前台 VpnService。服务在 `open_tun` 回调中应用 Builder 配置并持有 ParcelFileDescriptor，在 `protect_socket` 中调用 `protect(fd)`。

网络快照按串行队列更新。停止或授权撤销时，先销毁核心并等待其释放 FD 副本，再关闭宿主原 FD、排空网络任务和清除 underlying network。服务清理结束后才能重新启动。

## macOS 与 iOS

需要 macOS、Go 1.25.5+、完整 Xcode 工具链；构建宿主示例还需要 XcodeGen。最低系统版本为 iOS 15、macOS 13。

```bash
make mobile-tools
make apple
make apple-example
```

SDK 为 `dist/mobile/Libbox.xcframework`，包含 iOS 真机、iOS 模拟器和 macOS slices。示例构建脚本生成未签名 App 与扩展，仅执行编译和包结构检查。

| 项目 | iOS | macOS |
|---|---|---|
| 宿主产物 | `dist/examples/apple/Build/Products/Debug-iphoneos/LibboxHost.app` | `dist/examples/apple/Build/Products/Debug/MacHost.app` |
| 扩展 | `PlugIns/PacketTunnel.appex` | `Contents/Library/SystemExtensions/*.systemextension` |
| entitlement | `packet-tunnel-provider` | `packet-tunnel-provider-systemextension` |
| 出站选择 | 系统 NECP 管理 | 根据物理网络快照绑定接口 |

两端共用 [PacketTunnelProvider](../examples/apple/Shared/PacketTunnelProvider.swift)。App 通过 NETunnelProviderManager 启动 VPN，配置中保存 Start protobuf；macOS 还需激活系统扩展。部署时设置自己的 Team、Bundle ID 和匹配权限，并完成签名。

Provider 应用网络设置后，查找唯一的 utun FD 并借给 SDK。SDK 使用 CLOEXEC、非阻塞副本，不能同时启动另一条 `packetFlow` 读包循环。无法唯一识别 FD 时启动失败。

停止完成回调必须等核心调用、系统网络设置操作和启动流程退出；原 FD 由 NetworkExtension 管理。iOS 不固定物理 APN，也不接受 `default_network_strategy`。

### 内存与控制

AUTO profile 在 iOS NetworkExtension 中采用 40 MiB Go 软内存限制和 GOGC=50；macOS 保留桌面默认值。多个 SDK 实例共享进程级设置，最后一个使用该策略的实例销毁后恢复原值。该限制不是进程总 RSS 的硬上限。

宿主通过 `memory_pressure` 通知内存压力，建议使用有界事件队列。示例队列大小为 64，并使用显式 DoH 配置。

App/provider message 和 Android Binder 用于查询及运行时控制；创建、销毁、启动、停止、重载和网络快照更新由宿主生命周期管理。

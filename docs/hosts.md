# 四端最小宿主

示例均使用同一 protobuf schema。配置正文是 native sing-box JSON，
示例默认 direct 出站，便于独立验证宿主与核心。没有在构建脚本中自动启用 VPN。

## Windows x64

~~~bash
make windows
dotnet publish examples/windows/LibboxHost.csproj -c Release -r win-x64 --self-contained false -o dist/examples/windows
~~~

macOS 交叉编译需要 MinGW-w64 的 x86_64-w64-mingw32-gcc；可用 WINDOWS_CC 指定。
Windows 本机默认 gcc。DLL 不通过 int32 FD 接收 Wintun HANDLE；
native 模式由固定版本 sing-tun 创建/打开适配器、配置地址/路由并管理会话。
对应 Wintun 二进制已包含在核心依赖中。

.NET 示例要求 .NET 8+，从可执行文件目录加载 libbox.dll，避免工作目录 DLL 搜索。
LIBBOX_NATIVE_PATH 仅用于指定其他测试产物。
示例保持 DLL 加载到进程退出；更新 DLL 后重启宿主，会话本身可以反复创建和销毁。

~~~text
LibboxHost --smoke       # 只测试 protobuf/core 生命周期，无 TUN
LibboxHost --tun-smoke   # 提权终端：创建唯一命名 Wintun，不设置默认路由，然后停止
LibboxHost --tun         # 提权终端：完整示例 VPN，Ctrl+C 停止
LibboxHost config.json   # 运行自选配置
~~~

驱动加载、管理员权限、实际路由/DNS 恢复、休眠恢复和进程退出仍需 Windows 实机验收。
这里提供 DLL 内嵌宿主，不安装 Windows 服务，也不包含参考产品的用户 claim/takeover 策略。

## Android

~~~bash
make mobile-tools
make android
python3 scripts/build-android-example.py
~~~

需要 JDK 17、Android SDK、NDK；示例编译 SDK 35，最低 API 24。
Gradle 8.13 的下载在脚本中固定校验值。APK 输出到
examples/android/app/build/outputs/apk/debug/app-debug.apk。

Activity 显式请求 VPN 授权；VpnService 建立前台通知，维护物理网络快照，
在 HostRequest.open_tun 中应用 Builder 配置并持有 ParcelFileDescriptor，
在 protect_socket 中调用 protect(fd)，失败时返回 protobuf Error。
停止时先销毁核心，再关闭宿主原 FD。授权撤销走同一清理路径。
新的启动不会接管尚未清理完的旧实例；启动/停止使用 generation 防止迟到任务覆盖新会话。
网络快照及启动前的首次推送共用串行队列；停止会排空旧网络任务，再清除 underlying network。
更换 TUN 时先接管新 FD，旧 FD 关闭失败也不会泄漏新接口。

前台服务类型使用 systemExempted，适用于已获授权、已配置的 VPN；
相关系统规则见 [Android 文档](https://developer.android.com/develop/background-work/services/fgs/service-types)。
该示例不是完整商店发布工程，未包含产品订阅、后台保活策略或自动重连。

## Apple：macOS / iOS 共用 raw TUN FD

两端编译同一份 examples/apple/Shared 源码：

- PacketTunnelProvider.swift：启动/停止、protobuf、网络变化和内存压力。
- TunnelSettings.swift：把相同 TunOptions 映射为 NE 网络设置。
- AppleRawTunDescriptor.swift：有界扫描并严格识别唯一 utun FD。
- AppleHostSupport.swift：共享生命周期 generation 和可取消的设置等待。
- AppleHostPolicy.swift：统一 HOST、under_network_extension、OPEN_TUN 和 AUTO 内存配置。

核心适配同样共用 internal/singbox/fd_darwin.go。Apple 配置中的 TUN 必须采用 HOST raw FD；
NetworkExtension 恰好管理一个 TUN。NATIVE 模式仍可用于不含 TUN 的普通代理配置，
但不再作为 macOS VPN 示例模型。

参考 Hyper mobile / desktop 的共同顺序：

1. App 保存 Start protobuf，经 NETunnelProviderManager 启动扩展。
2. Provider 创建 MessageSession，提供物理网络快照。
3. 核心发出 HostRequest.open_tun，Provider 应用地址、路由、DNS、MTU。
4. setTunnelNetworkSettings 完成后查找唯一 utun，返回借用 FD。
5. 核心用 CLOEXEC、非阻塞副本读写；不与 packetFlow 读包循环并存。
6. 停止时先销毁核心并等待在途回调、系统网络设置回调与启动完成，再通知系统；Provider 不关闭原 FD。

FD 扫描采用 Hyper 移动端较严格的策略：按进程上限扫描，最多 65536 个描述符；
只接受 utun 后跟 ASCII 数字，零个或多个候选（包括重复别名）均拒绝。
Go 侧再次验证接口名，复制到标准输入输出之外的 FD；失败只关闭自己的副本。

生命周期拒绝并行启动，停止会废弃旧 generation。超时/取消后的网络设置回调不会
重新唤醒启动流程，也不会把旧启动结果提交到新会话。
同步等待的超时不会提前释放系统设置操作的计数；失败清理会先排空旧设置操作，
再清空网络设置。首次网络快照与后续变化按同一队列顺序发送。

### 部署入口保留平台区别

| 项目 | iOS | macOS |
|---|---|---|
| App | 共用 SwiftUI 控制器 | 同一控制器，额外请求系统扩展激活 |
| Provider | PacketTunnel.appex | Network System Extension |
| 目录 | App 内 PlugIns | App 内 Contents/Library/SystemExtensions |
| NE entitlement | packet-tunnel-provider | packet-tunnel-provider-systemextension |
| 出站 | NECP 系统管理，不固定蜂窝 APN | 绑定当前物理接口以避免回流 TUN |
| AUTO 内存策略 | Go 软限制 40 MiB、GOGC=50 | 保留桌面默认值 |

这些差异在部署与平台策略中处理，protobuf 和 raw FD 所有权模型保持一致。
macOS App 只在点击 Start 后请求 OSSystemExtensionManager 激活；
用户停止或启动被替代后，迟到的激活结果不会继续启动 VPN。

### 仅编译与单元测试

~~~bash
make apple
make apple-host-tests
make apple-example
~~~

apple-host-tests 在 macOS 运行纯逻辑/模拟描述符测试，不创建 utun。
apple-example 只编译 iOS iphoneos App/Extension 与 macOS App/System Extension，
不执行 xcodebuild test，不启动模拟器，不签名、不安装、不激活或运行 VPN。

输出：
- dist/examples/apple/Build/Products/Debug-iphoneos/LibboxHost.app
- dist/examples/apple/Build/Products/Debug/MacHost.app
- MacHost.app/Contents/Library/SystemExtensions/com.hyperpteltd.libbox.example.macos.tunnel.systemextension

SDK XCFramework 仍可包含模拟器 slice，编译该 slice 不会运行模拟器。
实际部署使用自己的 Team/Bundle ID 和匹配的权限；构建脚本不代替配置签名。

### 低内存、DNS 与控制

Apple 移动端编译 with_low_memory。AUTO 在 iOS NetworkExtension 中选择 40 MiB Go 软限制和
GOGC=50；macOS 不强制沿用手机限制。两端都使用有界日志队列和 protobuf memory_pressure。
限制由所有 SDK 实例协调，最后一个实例销毁后恢复进程原值；它不是整个进程 RSS 的硬限制。

样例使用显式 DoH，不伪造系统 DNS。App 的 provider message 只能查询和控制运行中的核心，
创建/销毁核心及网络设置仍由 NE 生命周期管理。没有逐包 packetFlow 桥接或第二个 TUN 读取器。

本轮按要求不执行真机、模拟器或真实 VPN 测试。验证结论限于源码检查、模拟 FD/生命周期
单元测试、Go 测试与未签名编译，不把它们写成真实系统隧道验证。

## 宿主逻辑测试与清理

`make apple-host-tests`、`make android-host-tests`（JDK 17）、`make windows-host-tests`（.NET 8+）
分别运行 Apple 设置回调/生命周期、Android FD 所有权、.NET 请求克隆/并发与截止时间测试。
这些测试不需要设备、模拟器或 VPN 权限。

`make clean` 删除本仓库的 dist、_build、平台构建目录、生成的 Xcode 项目、tools/bin 和 Python 缓存。
源码、已纳入工程的 protobuf 生成代码、全局工具链及全局包缓存保留；之后重新构建需按文档安装本地构建工具。

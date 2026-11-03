# 四端宿主与 protobuf 生命周期检查

完成日期：2026-11-03。本记录描述首次提交前的检查；后续核心版本以 go.mod 和 README 为准。
范围为本地源码、生命周期/并发回归测试和交叉编译；不运行真机、模拟器或系统 VPN。

## 修复

| 位置 | 问题与修复 |
|---|---|
| Go engine / MessageSession | 接受停止意图后，即使请求截止时间到期，也完成停止清理；关闭开始即拒绝新工作，等待在途调用和回调退出。启动失败保留清理错误。 |
| HOST 网络更新 | 网络更新使用独立串行锁，能到达正在启动的实例；实例发布/销毁与更新调用计数同步，避免启动持有生命周期锁阻塞网络恢复。 |
| C ABI | 句柄保留到 Close 完成，使并发重复 session_free 都等待回调退出，避免宿主提前释放 native context。 |
| Android | 首次和后续网络快照共用串行队列；停止排空旧网络任务并清除 underlying network。替换原 TUN FD 时先接管新值，旧值关闭失败不再泄漏新值；SDK 副本分配避开标准输入输出。 |
| macOS / iOS 共用 Provider | 区分“取消同步等待”与“系统设置操作真正完成”。停止等待系统设置回调和启动完成；失败后先排空旧设置，再清空配置。旧 generation 的回调不能提交到新会话；首次网络快照不再覆盖较新的路径状态。 |
| .NET 客户端 | 克隆调用方 Request 后分配 id/预算，保留更早或已过期的截止时间；并发 Dispose 等待同一次释放。 |
| 模式切换 | 固定上游模式管理器存在无锁字符串读写。SDK 在启动前提供不可变模式实例，通过受控重启切换；拒绝旁路 API 控制器。补测并发流量、generation 和实际路由效果。 |

`set_mode` 会中断现有连接，重建流量/测速等运行状态，并采用与 reload 一致的失败恢复流程。
模式在同一 engine 内保留；destroy 后使用配置默认值，不使用上游缓存模式。详见 [API](api.md)。

## 本地验证

- 47 个 Go 测试函数所在的完整套件通过，标准构建标签下 race、vet 通过；Go 1.25.5 通过。
- Go / Java / C# / Python / Swift protobuf 生成代码一致性通过，公开 API 与线格式检查通过。
- Apple 生命周期、模拟 FD、设置回调排空测试通过；Android FD 所有权测试通过；.NET 请求并发与截止时间测试通过。
- macOS C protobuf/反向宿主回调和 .NET protobuf 生命周期冒烟通过，仅使用本地代理或模拟失败回调。
- Windows x64 DLL 交叉构建与 .NET 发布、Android AAR/APK、Apple XCFramework 构建通过。
- iOS App/PacketTunnel.appex 与 macOS App/System Extension 未签名编译及 bundle 结构检查通过。没有启动模拟器。
- Linux amd64、CGO_DISABLED 核心编译通过。

这些结果不代表 Windows Wintun、Android VPN 或 Apple NetworkExtension 的真实系统验收。
远程 CI 未运行。固定核心源码未修改；检查完成时本仓库尚未提交或推送。

## 构建产物清理

验证后使用 `make clean` 清理约 3.60 GiB 的本仓库产物与缓存，包括 dist、_build、Android/.NET 构建目录、
生成的 Xcode 项目、本地 tools/bin 和 Python 缓存。清理时发现 Android 根 build 目录的 Gradle 问题报告
未被忽略，已补齐规则；重新生成宿主逻辑测试产物并清理，核对非忽略源码文件的 SHA-256 汇总值不变。
保留源码、protobuf 生成代码、全局工具链与全局包缓存。重建与本机测试入口见 [宿主说明](hosts.md)。

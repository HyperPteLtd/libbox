# 协议设计

统一控制协议位于 proto/libbox/v1/control.proto，完全由本 SDK 维护。
不使用 sing-box daemon 的 protobuf，也不依赖其 gRPC 服务类型。

## 分层

- MessageSession：语言绑定对象、核心实例的创建/销毁与宿主消息处理器。
- internal/control：请求校验、预算、oneof 分发、响应与事件转换。
- internal/contract：内部数据结构和错误语义，不含序列化或上游类型。
- internal/backend：私有运行时接口。
- internal/singbox：唯一直接依赖上游 Go 类型的适配实现。
- protocol/v1：生成的公共 protobuf 类型。
- cmd/libboxc：仅做指针/长度检查、句柄路由与内存管理。

参考桌面 core 采用“核心适配 → 分发器 → Windows 管道/Apple provider message”。
其实际内核是 mihomo；本项目借鉴分层、字段版本、请求标识和描述符所有权，
保留 sing-box 的独立实例模型，不移植产品级配置编排或服务用户所有权。

## 四端传输

| 宿主 | 控制消息 | 宿主反向消息 |
|---|---|---|
| Android | Java MessageSession.handleMessage(byte[])；示例 Binder 转发 | 一个 HostMessageHandler |
| Windows | DLL session_call，.NET 编解码 protobuf | native Wintun 不需要回调 |
| macOS | App 通过 provider message 发送 protobuf；共享 Provider 转发到 MessageSession | 与 iOS 共用 raw FD 宿主处理器 |
| iOS | App 通过 provider message 发送 protobuf；共享 Provider 转发到 MessageSession | 扩展处理 open_tun，借出原 FD |

Apple 共用示例把初始 Start protobuf 数据放进 VPN 的 providerConfiguration。系统生命周期仍使用
平台要求的 VpnService/NETunnelProviderManager API；macOS 的系统扩展激活是部署步骤，没有自定义 JSON 消息。

可选 protocol/frame 提供流式传输帧：LBXP magic、u16 版本、u16 kind、u32 长度，
均为小端，总共 12 字节。请求 kind=1、响应 kind=2，长度先校验再分配。
C 调用和 provider message 已有消息边界，不加该帧。
帧工具不提供身份认证，也不自动启动监听器；部署特权 IPC 服务时，宿主要自行限定授权调用方。

## 兼容性

- v1 字段编号不复用；删除的编号保留 reserved。
- 不兼容变化需要协议主版本升级；兼容新增字段不会改变原生调用签名。
- 未识别的字段由 protobuf 保留/忽略；未知 method 明确失败。
- Request、HostRequest、Response、HostResponse 都包含版本和 id。
- 配置正文仍是核心原生 JSON；升级核心时独立验证配置格式。

生成工具固定 protoc 33.4、Go protobuf 版本见 go.mod、SwiftProtobuf 1.32.0。
工具下载校验 SHA-256；生成的 Go/Java lite/C#/Python/Swift 文件提交到仓库。

~~~bash
python3 scripts/generate-protocol.py
python3 scripts/generate-protocol.py --check
bash scripts/install-swift-protobuf.sh
python3 scripts/generate-protocol.py --swift --check
~~~

公开 Go 接口、C 导出函数、请求字段编号和 get_status 线格式有测试；
再用 C、.NET、Swift 的实际调用验证跨语言序列化与内存释放。

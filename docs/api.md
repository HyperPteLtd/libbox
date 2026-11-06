# API

控制协议为 protobuf v1，完整消息定义见 [control.proto](../proto/libbox/v1/control.proto)。`Config.config_json` 按固定的 sing-box v1.14.4 配置格式解释，另外支持 SDK 自带的 [`loadbalance` 出站组](#loadbalance-出站组)。

## 会话与生命周期

Go/gomobile 入口：

```go
type HostMessageHandler interface {
    HandleHostMessage(request []byte) ([]byte, error)
}
func NewMessageSession(handler HostMessageHandler) *MessageSession
func (*MessageSession) HandleMessage(request []byte) []byte
func (*MessageSession) Close() error
```

使用 `NewMessageSession` 创建会话；gomobile 自动生成的无参构造函数不能初始化有效会话。NATIVE 网络模式可以传入空宿主处理器。

1. `get_info` 可在创建核心前读取版本和能力。
2. `create_engine` 创建一个核心实例；同一会话同时只持有一个实例。
3. `check_config` 校验配置，`start` 启动；`stop` 停止后可以再次启动。
4. `destroy_engine` 销毁核心后可以再次创建；`Close` 永久关闭消息会话。

调用可并发执行，核心生命周期变更串行处理。关闭会拒绝新工作，并等待在途核心调用和宿主回调退出。`Close` 幂等，也会销毁尚未释放的核心。

## 请求与响应

每个 `Request` 设置 `protocol_version=1`、调用方分配的 `id` 和一个 `method`。`Response` 回显版本与 id，结果由 `oneof result` 表示；调用方应核对版本、id 并处理 `Error.code`。

| 项目 | 规则 |
|---|---|
| 控制请求、响应 | 最大 4 MiB，原始 protobuf 字节 |
| `timeout_ms` | 默认 30000，最大 60000 |
| `deadline_unix_ms` | 若提供更早的有效绝对截止时间，则采用该时间；过期请求不执行 |
| 宿主响应 | 最大 64 KiB |
| 未知字段 | 按 protobuf 兼容规则处理 |
| 未知方法或主版本 | 返回明确错误 |

排队中的启动、重载受请求预算约束；成功启动后的运行生命周期独立于原请求截止时间。原生系统调用和同步宿主回调无法强制抢占。停止意图一旦接受，即使请求随后超时，也会完成清理。

应用错误由 `Response.error` 返回。`HostMessageHandler` 的语言级 error 用于桥接故障，宿主操作失败应返回 `HostResponse.error`；`Close` 的语言级 error 表示清理失败。

## 核心选项

| `EngineOptions` 字段 | 规则 |
|---|---|
| `working_directory` | 必填绝对路径；各运行实例使用独立目录 |
| `temp_directory` | 默认工作目录下的 `tmp` |
| `network_mode` | `NATIVE` 由核心管理网络；`HOST` 接收宿主网络快照和回调 |
| `event_buffer` | 默认 256，范围 1–4096 |
| `under_network_extension` / `include_all_networks` | 按 Apple 宿主实际配置设置 |
| `memory_profile` | `AUTO`、`DEFAULT` 或 `NETWORK_EXTENSION` |
| `memory_limit_bytes` | 0 表示采用 profile 默认值；显式值至少 8 MiB |
| `gc_percent` | 0 表示采用 profile 默认值；显式值为 1–1000 |
| `host_capabilities` | `OPEN_TUN`、`PROTECT_SOCKET`、`CONNECTION_OWNER`；需使用 HOST 模式并提供处理器 |

各平台的网络模式、TUN 栈和内存策略见 [平台接入](hosts.md)。

## 控制方法

| 方法 | 行为 |
|---|---|
| `create_engine` / `destroy_engine` | 创建、销毁核心，保留消息会话 |
| `check_config` | 构造并关闭校验实例，不启动监听器或 TUN |
| `start` / `stop` | 启停核心；重复启动报错，重复停止安全；停止会取消在途测速 |
| `reload` | 先校验，再替换实例；启动失败尝试恢复旧配置；中断已有连接 |
| `get_info` / `get_status` | 版本、能力、状态、流量和 Go 内存数据 |
| `list_groups` / `list_outbounds` | 代理组与出站；未测速时 `delay_ms` 不存在；`loadbalance` 组的 `selected` 为代表节点 |
| `select_outbound` | 切换 selector 成员；失败时保留原选择 |
| `url_test` | 对指定 HTTP(S) URL 发起 HEAD 测速，返回毫秒延迟 |
| `set_mode` | 切换配置允许的模式，通过受控重启完成；相同模式不重启 |
| `set_paused` | 设置设备暂停状态 |
| `close_connections` | 关闭被跟踪的活动连接 |
| `next_event` | 获取一个事件；等待超时返回 Empty，销毁唤醒等待者 |
| `update_network` | 完整替换宿主物理网络和 DNS 快照；`default_interface=-1` 表示离线 |
| `memory_pressure` | `NONE`、`WARNING`（GC）、`CRITICAL`（GC 并释放闲置页） |

`set_mode` 与 `reload` 均会中断已有连接，重建流量、测速历史和未持久化的节点选择等运行状态。模式在同一 engine 内保留；销毁后使用配置默认值，不读写上游缓存模式。配置不接受 `external_controller` 或 `api` service。

`get_status.state` 为 `stopped`、`starting`、`running`、`stopping`、`reloading` 或 `closed`。每次成功启动（含模式切换、回滚）都会增加 `generation`，流量按当前 generation 统计。Go 内存数据属于整个进程。

## loadbalance 出站组

`loadbalance` 是 SDK 在固定的 sing-box 核心上注册的出站组类型，未修改或替换上游模块。`check_config`、`start`、`reload` 都识别该类型；`get_info.capabilities` 含 `loadbalance_group` 时可用。

```json
{ "type": "loadbalance", "tag": "JP", "outbounds": ["jp-1", "jp-2", "jp-3"],
  "strategy": "consistent-hashing", "url": "https://www.gstatic.com/generate_204",
  "interval": "3m", "tolerance": 150, "fallback": "HK", "interrupt_exist_connections": true }
```

| 字段 | 规则 |
|---|---|
| `outbounds` | 必填，成员出站或组的标签；不能重复，不能包含组自身 |
| `strategy` | 只支持 `consistent-hashing`，可省略；其他值使配置无效 |
| `url` | 健康检查地址，绝对 HTTP(S) URL；默认 `https://www.gstatic.com/generate_204` |
| `interval` | 健康检查周期，默认 `3m`；不得大于 `idle_timeout` |
| `idle_timeout` | 组内无新连接超过该时长后暂停周期检查，默认 `30m`；再次有连接时立即补测 |
| `tolerance` | 毫秒。大于 0 时只在延迟不超过最快健康成员加该值的成员间分配；0（默认）不按延迟筛选 |
| `recovery_checks` | 故障成员连续通过多少轮检查后重新加入，默认 2 |
| `fallback` | 可选，组内没有健康成员时使用的出站或组，例如另一个国家组 |
| `interrupt_exist_connections` | 成员被判定故障、或兜底解除时，同时断开应用的现有连接 |

**分配。** 连接按目标站点做一致性哈希（rendezvous 哈希）：有域名时取可注册域名（eTLD+1，例如 `img.example.co.uk` 取 `example.co.uk`），目标是 IP 但路由已嗅探或解析出域名时使用该域名，否则使用目标 IP。同一站点稳定使用同一成员，不同站点分散到各成员。成员增减只影响少量站点：移除的成员把它的站点交给各站点的下一顺位，新增的成员只接手约 1/n 的站点。UDP 按 `ListenPacket` 或 UDP 拨号时的目标地址分配，不支持该网络的成员不参与。

**健康与故障转移。** 启动后先检查一轮，之后在组有流量时按 `interval` 检查；网络变化后立即重测。拨号失败的成员立即停止分配，同时触发一轮检查。选中成员拨号失败时，按该站点的哈希顺序换下一个健康成员，单次连接最多尝试 3 个出站。重试只发生在向调用方返回连接之前，此时应用数据尚未交给任何出站，不会重复发送；连接建立后的失败不重试。调用方取消的拨号不算成员故障。

**恢复。** 故障成员需连续通过 `recovery_checks` 轮检查（包括拨号失败后立即触发的那一轮）才重新加入，避免站点出口反复切换；任一轮失败重新计数。网络变化后的那一轮只需一次成功。

**兜底。** 没有健康成员时新连接走 `fallback`；兜底也失败或未配置时，按哈希顺序尝试故障成员。两个组可以互为兜底，同一次拨号不会循环。成员恢复后兜底解除。

**断开现有连接。** 成员被判定故障时，经该成员的内部连接（如 DNS 或其他出站以该组为 detour 的连接）总是断开；应用连接只在 `interrupt_exist_connections` 为 true 时断开。兜底解除时，对经兜底的连接按同样规则处理。成员恢复或延迟变化不会断开连接。

**展示。** `list_groups` 中该组 `selectable=false`，`outbounds` 为成员，`selected` 为代表节点：最快的健康成员；兜底期间为兜底标签。组自身的延迟为代表节点的延迟，兜底期间不存在。`url_test` 可以对组或成员测速；对组测速时按测试地址的站点选择成员。`select_outbound` 不适用于该组。

## 宿主回调与 FD

核心发送带版本、id 和预算的 `HostRequest`，宿主返回对应的 `HostResponse`。

| 回调 | 宿主职责 |
|---|---|
| `open_tun` | 应用地址、路由、DNS、MTU 等设置后，返回借用的原 FD |
| `protect_socket` | 对给定 FD 执行保护或绑定；成功返回 Empty，失败中止拨号 |
| `find_connection_owner` | 根据源/目标地址和端口返回连接归属 |

SDK 同步复制 TUN FD 并管理副本；宿主保持原 FD 有效，直至核心清理完成。FD 只能在嵌入核心的同一进程内使用。Windows Wintun 使用 NATIVE 模式，不通过该 Unix FD 接口传递 HANDLE。

回调可能并发并来自 Go 线程，宿主应遵守预算，且不能同步重入生命周期操作。回调处理器及其 native context 必须保持有效，直到会话释放返回。

v1.14.4 使用 `process_paths` 的首项作为进程路径，`package_names` 用于应用包名匹配。DNS 搜索域保存在宿主网络快照中，用于本地 DNS 环境。

## 事件与错误

事件包含 `Log`、`StateChange` 或 `GroupHealth`。队列满时丢弃最旧项并增加 `dropped_events`；日志最长 16 KiB，裁剪后设置 `truncated`。事件队列不是广播，多个消费者会分走事件；运行状态以 `get_status` 为准。

`GroupHealth` 在 `loadbalance` 组的成员健康变化时发送，兜底的启用和解除也随之发送。每个事件都是该组的完整快照：`healthy` 为接收新连接的成员，`unhealthy` 为故障或仍在恢复期的成员，`fallback` 在兜底期间为兜底标签、否则为空。旧事件被丢弃时，以该组最新的事件为准。不认识该分支的旧宿主会收到未设置 `data` 的事件，应忽略。

按错误码处理失败，不依赖 message 文本。常见错误包括 `invalid_argument`、`unsupported`、`not_running`、`closed`、`canceled`、`host_error`、`start_failed` 和 `stop_failed`。`reload_failed` 表示旧配置已恢复；`rollback_failed` 表示恢复失败，核心已停止。未知错误码按一般失败处理。

## C ABI

以 [libbox.h](../include/libbox.h) 为接口定义，动态库提供四个函数：

```c
uint64_t libbox_session_new(void *context, libbox_host_message_handler callback);
void *libbox_session_call(uint64_t session, void *request, uint64_t request_length,
                         uint64_t *response_length);
void libbox_free_buffer(void *value);
void libbox_session_free(uint64_t session);
```

`session_new` 只分配传输句柄，核心通过 `create_engine` 初始化。`session_call` 的输入在调用期间有效，`response_length` 必须非空；返回缓冲区由 SDK 分配，必须且只能用 `libbox_free_buffer` 释放一次。

宿主回调签名：

```c
typedef int64_t (*libbox_host_message_handler)(
    void *context, const void *request, uint64_t request_length,
    void *response, uint64_t response_capacity);
```

回调向容量为 65536 的输出区写入 HostResponse，返回字节数；负数表示桥接失败。所有输入、输出指针仅在调用期间有效。

先发送 `destroy_engine` 可收到清理错误，再用 `session_free` 释放句柄。并发重复释放会等待同一次清理；清理期间新请求返回 `closed`，释放完成后返回 `invalid_handle`。句柄属于创建它的库实例，不能跨库使用。

.NET 接入示例见 [CoreClient.cs](../examples/windows/CoreClient.cs)。

## 流式传输

[protocol/frame](../protocol/frame) 提供可选的 12 字节消息头：`LBXP`、u16 版本 1、u16 kind、u32 长度，数字采用小端。请求 kind=1，响应 kind=2，载荷为 1 字节至 4 MiB。

C 调用和 Apple provider message 已有消息边界，直接使用原始 protobuf。自行建立特权 IPC 服务时，宿主负责调用方认证。

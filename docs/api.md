# Protobuf API v1

四端只有一个应用协议：`proto/libbox/v1/control.proto`。不提供 JSON 控制入口、按操作拆分的 C 函数或公开的上游类型。

## 会话

Go/gomobile 只导出：

~~~go
type HostMessageHandler interface {
    HandleHostMessage(request []byte) ([]byte, error)
}
func NewMessageSession(handler HostMessageHandler) *MessageSession
func (*MessageSession) HandleMessage(request []byte) []byte
func (*MessageSession) Close() error
~~~

原生构造函数仅分配会话，不创建核心。所有配置与操作通过 protobuf：

1. 可先发送 get_info，读取版本和能力。
2. create_engine：传 EngineOptions。每个会话拥有一个核心实例。
3. check_config / start / reload / get_status 等。
4. destroy_engine：停止核心并回收资源，可再次 create_engine。
5. Close：释放语言绑定会话；幂等，也会清理遗漏销毁的核心。

不要使用 gomobile 自动生成的无参构造函数。Native Close 的错误用于资源清理失败；
应用操作的错误始终由 Response.error 表达。HostMessageHandler 的 error 仅用于原生桥接故障，
正常宿主失败应编码为 HostResponse.error。

## 请求与响应

Request 包含 protocol_version=1、调用方生成的 id、timeout_ms、deadline_unix_ms 和 oneof method。
Response 回显版本/id，oneof result 包含结果或 Error。调用方必须核对版本与 id。

timeout_ms 默认 30000，上限 60000；绝对截止时间若更早，则采用它。已过期请求不执行。
排队中的启动/重载可取消，不会在调用方超时后悄悄开始。成功启动后的核心生命周期独立于
原请求，原请求的截止时间不会停止已运行的核心。原生系统调用和同步宿主回调无法强制抢占，
宿主需要遵守回调预算；资源清理一旦开始会完成。
Stop 一旦接受并取消运行上下文，即使请求随后超时也会等到停止清理完成；
Close 开始后拒绝新请求，并等待正在执行的核心调用与宿主回调退出。

消息上限 4 MiB，解析前检查长度；无效消息也返回 protobuf Error。事件队列不是广播，
多个消费者会分走事件。未知字段忽略，未知方法/主版本明确报错。

## EngineOptions

| 字段 | 规则 |
|---|---|
| working_directory | 必填绝对路径，各运行实例使用独立目录 |
| temp_directory | 默认工作目录下 tmp |
| network_mode | NATIVE：Windows/Linux 核心 TUN 或纯代理；Apple TUN 必须 HOST，宿主管理网络并提供 raw FD |
| event_buffer | 默认 256，范围 1–4096；移动扩展示例使用 64 |
| under_network_extension / include_all_networks | 按真实 Apple 宿主设置 |
| memory_profile | AUTO、DEFAULT、NETWORK_EXTENSION |
| memory_limit_bytes / gc_percent | 可选 Go 运行时限制；见宿主文档 |
| host_capabilities | HOST 模式按需声明 OPEN_TUN、PROTECT_SOCKET、CONNECTION_OWNER |

声明能力时必须提供 HostMessageHandler。Windows TUN 采用 NATIVE；Unix FD 不能表示
Wintun HANDLE。macOS/iOS 的 TUN 统一采用 HOST raw FD，每个 NetworkExtension 配置恰好一个 TUN。
HOST 模式保留接口选择和保护控制，拒绝配置中的显式 bind_interface、
inet4_bind_address、inet6_bind_address、default_interface、protect_path、netns。
include_all_networks 不接受 system/mixed 栈，使用默认 go 或可选 gvisor。
iOS NetworkExtension 的出站使用系统 NECP 路由，不固定物理 APN；macOS 根据物理网络快照绑定出站。
因此 iOS NE 不启用 auto_detect_interface，也不接受 default_network_strategy；两端的数据面仍共用 raw FD。

## 方法

| 方法 | 语义 |
|---|---|
| create_engine / destroy_engine | 创建/销毁核心，不销毁消息会话 |
| check_config | 构造并关闭校验实例，不打开监听器/TUN |
| start / stop | 启停；Stop 可取消在途测速；重复启动报错 |
| reload | 先校验；启动失败尝试恢复旧配置；中断现有连接 |
| get_info / get_status | 版本/能力或运行状态、流量和 Go 内存数据 |
| list_groups / list_outbounds | 自有 protobuf 结构；未测速时 delay_ms 不存在 |
| select_outbound | 选择 selector 中的成员，失败不改变选择 |
| url_test | 显式 HTTP(S) HEAD URL，返回延迟毫秒 |
| set_mode | 使用配置允许的模式；通过受控重启切换，中断现有连接 |
| set_paused | 设置设备暂停状态 |
| close_connections | 关闭跟踪的活动连接 |
| next_event | 等待一个事件；超时返回 Empty；destroy 唤醒等待者 |
| update_network | 完整替换宿主物理网络快照，包括 DNS；-1 表示离线 |
| memory_pressure | NONE、WARNING（GC）、CRITICAL（GC 并释放闲置页） |

Config.config_json 是原生 sing-box 配置内容，并非另一个控制协议。它按固定核心版本解释，
不承诺跨核心版本自动迁移。所有其他控制参数和宿主参数都有 protobuf 类型。

固定上游的模式管理器存在无锁读写；SDK 用不可变模式实例和与 reload 相同的停止/回滚流程
完成 set_mode。相同模式不重启，新模式成功后 generation 递增；流量、测速历史和未持久化的
节点选择等运行状态随实例重建。模式在同一 engine 内保留，destroy_engine 后回到配置默认值。
不读取或写入上游缓存中的模式。拒绝 external_controller 和 api service，避免旁路修改生命周期与模式。

get_status 的 state 为 stopped、starting、running、stopping、reloading、closed；generation 每次成功
启动（包括模式切换、回滚）递增。流量是当前 generation 的累计值。go_memory_limit_bytes 和
go_heap_alloc_bytes 是进程级 Go 数据，不是单实例或进程总 RSS。

## 双向宿主消息

核心通过唯一回调发送 HostRequest，并接收 HostResponse；双方都检查版本/id。

- open_tun：携带地址、MTU、DNS、路由、应用过滤和可选 HTTP 代理。宿主应用系统设置后返回
  TunDescriptor.fd。FD 为借用，SDK 同步复制；SDK 关闭副本，宿主负责原 FD。
- protect_socket：宿主对给定 FD 执行保护/绑定，返回 Empty 表示成功；失败中止拨号。
- find_connection_owner：根据地址/端口返回自有 ConnectionOwner。

回调只能发生在嵌入核心的同一进程。不能把 FD 整数通过远程 UI/管道当作可用描述符传递。
回调可能并发、可能来自 Go 线程，不保证主线程；不要在回调中同步重入生命周期操作。
销毁会等待在途回调并拒绝迟到回调，因此原生 context 必须保持到会话释放返回。
宿主响应上限 64 KiB。

## 事件与错误

事件是 typed Log 或 StateChange。队列满时丢弃最旧项并增加 dropped_events；
日志限制为 16 KiB，truncated 表示被裁剪。事件只提供通知，权威状态以 get_status 为准。

错误码包括 invalid_argument、unsupported_version、unknown_method、uninitialized、invalid_state、
not_running、closed、unsupported、not_found、start_failed、stop_failed、reload_failed、
rollback_failed、canceled、host_error、io_error、core_error、internal_error、response_too_large。
C 传输另有 invalid_handle。不要根据 message 文本分支，未知错误码按一般失败处理。

reload_failed 表示旧配置已恢复；rollback_failed 表示恢复失败，核心已停止。

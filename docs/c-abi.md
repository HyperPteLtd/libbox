# Protobuf C ABI v1

头文件为 include/libbox.h。DLL/动态库仅导出四个函数：

~~~c
uint64_t libbox_session_new(void *context, libbox_host_message_handler callback);
void *libbox_session_call(uint64_t session, void *request, uint64_t request_length,
                         uint64_t *response_length);
void libbox_free_buffer(void *value);
void libbox_session_free(uint64_t session);
~~~

session_new 仅分配不透明传输句柄。Windows native 宿主传 NULL 回调，然后通过 create_engine
protobuf 消息初始化。session_call 输入/输出都是原始 protobuf，不含 NUL 终止符或 stream 帧头。

request 只需在调用期间有效；response_length 必须非空。返回缓冲区属于 SDK，必须用
libbox_free_buffer 释放一次，不能使用宿主分配器。请求/结果上限 4 MiB；失败也编码为 Response。

先发送 destroy_engine 可以收到清理错误，再调用 session_free 释放句柄。忘记 destroy 时，
session_free 仍会等待核心和在途回调结束。句柄不复用；会话释放后调用返回 invalid_handle。
并发的重复 session_free 也会等待同一次清理；清理期间新请求返回 closed。
没有全局或线程局部 last-error 槽。

## 唯一宿主回调

~~~c
typedef int64_t (*libbox_host_message_handler)(
    void *context, const void *request, uint64_t request_length,
    void *response, uint64_t response_capacity);
~~~

输入是 HostRequest，向 SDK 提供的输出区写 HostResponse。返回字节数；负数表示桥接失败。
输出容量为 65536，不能越界。正常操作失败应写入 protobuf Error。

输入/输出指针均为短期借用。context 与函数指针保持有效，直到 session_free 返回。
回调会运行在 Go 线程上，宿主要正确固定托管委托/context，禁止同步重入生命周期操作。
描述符只能在同一进程使用。

.NET 可运行示例：examples/windows/CoreClient.cs。
原生传输与反向回调测试：scripts/smoke-protobuf.py。
旧 JSON、libbox_create/libbox_call、分散的回调 setter 等入口已删除。

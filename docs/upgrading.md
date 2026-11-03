# 升级核心与维护协议

公开接口只包含 MessageSession、HostMessageHandler 和四个 C 传输函数。
后续功能通过 protobuf 扩展，不增加按操作拆分的 FFI 方法。

1. 固定 sing-box commit 的完整 pseudo-version，更新 go.mod/go.sum。
2. 在 internal/singbox 适配上游变更；不让上游类型离开该目录。
3. 验证原生配置样本。SDK 协议稳定不保证核心 JSON 配置格式跨版本不变。
4. 运行默认测试、标准标签下 race/vet、协议生成检查、API/字段编号检查。
5. 构建全部平台产物与最小宿主，运行原生 protobuf 冒烟。
6. 在目标设备验证 VPN、DNS、网络切换、后台、休眠恢复和资源清理。

~~~bash
go mod download -json github.com/sagernet/sing-box@COMMIT
go get github.com/sagernet/sing-box@PSEUDO_VERSION
go mod tidy
make test race vet protocol-check
~~~

同步内部版本回退字符串和 README 的 core pin。不要发布浮动分支或本地 replace。

testdata/api-v1.txt 固定 Go/gomobile 表面，C 导出列表和 protobuf 方法编号也有检查。
项目首次发布前已按要求删除旧 JSON/旧 C ABI；当前 protobuf v1 是新的契约基线。
UPDATE_API=1 只用于审查过的兼容新增，不能自动接受已有接口删除或修改。
删除 proto 字段要保留编号，不把不同含义塞进旧编号或二进制载荷中。

生成代码来自 proto/toolchain.json 固定的工具链；不要手工修改生成文件。
CI 的构建/冒烟与真实设备验收分开记录，不把未运行的平台写成已验证。

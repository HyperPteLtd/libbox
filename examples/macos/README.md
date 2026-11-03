# macOS 宿主

macOS 已与 iOS 共用 `../apple/Shared` 中的 raw TUN FD Provider，不再使用独立 native-TUN CLI。

构建 `../apple/project.yml` 的 `MacHost` scheme：App 嵌入 Network System Extension，
点击 Start 后才请求系统激活与 VPN 启动。构建脚本只编译，不安装、不激活、不运行。

核心只读写借用原 FD 的副本，NetworkExtension 负责原 FD、地址、路由、DNS 和隧道生命周期。

import SwiftUI

@main struct HostApp:App {
    var body:some Scene {
        WindowGroup {
            #if os(macOS)
            HostView().frame(minWidth:440,minHeight:300)
            #else
            HostView()
            #endif
        }
    }
}
struct HostView:View {
    @StateObject private var controller=AppleTunnelController()
    var body:some View {
        VStack(spacing:20) {
            Text("Libbox raw TUN host").font(.title)
            Text(controller.message).font(.footnote).textSelection(.enabled)
            Button("Start VPN",action:controller.start).disabled(controller.busy)
            Button("Get protobuf status",action:controller.status)
            Button("Stop VPN",action:controller.stop)
        }.padding()
    }
}

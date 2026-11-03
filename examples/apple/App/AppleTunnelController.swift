import Foundation
import NetworkExtension
import SwiftProtobuf
import Combine

final class AppleTunnelController: ObservableObject {
    @Published private(set) var message="Ready. Start requests system approval."
    @Published private(set) var busy=false
    private var manager:NETunnelProviderManager?
    private var generation:UInt64=0
    private var requestID:UInt64=0
    #if os(macOS)
    private let activation=SystemExtensionActivator()
    #endif

    private func onMain(_ work:@escaping()->Void) {
        if Thread.isMainThread { work() } else { DispatchQueue.main.async(execute:work) }
    }
    func start() {
        guard !busy else { return }
        generation &+= 1
        let ticket=generation
        busy=true;message="Preparing NetworkExtension"
        guard let bundle=Bundle.main.bundleIdentifier else { finish(ticket,HostControllerError("Missing app identifier"));return }
        let provider=bundle+".tunnel"
        NETunnelProviderManager.loadAllFromPreferences { existing,error in
            self.onMain {
                guard self.generation==ticket else { return }
                if let error { self.finish(ticket,error);return }
                let selected=existing?.first { ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier==provider } ?? NETunnelProviderManager()
                self.manager=selected
                if [.connected,.connecting,.reasserting].contains(selected.connection.status) {
                    self.busy=false;self.message="Tunnel is already active";return
                }
                #if os(macOS)
                self.message="Activating the network system extension"
                self.activation.activate(identifier:provider,onApproval:{
                    guard self.generation==ticket else { return }
                    self.message="System approval is required for the network extension"
                }) { error in
                    guard self.generation==ticket else { return }
                    if let error { self.finish(ticket,error) } else { self.configure(selected,provider:provider,ticket:ticket) }
                }
                #else
                self.configure(selected,provider:provider,ticket:ticket)
                #endif
            }
        }
    }
    private func configure(_ selected:NETunnelProviderManager,provider:String,ticket:UInt64) {
        let configuration=NETunnelProviderProtocol()
        configuration.providerBundleIdentifier=provider
        configuration.serverAddress="Libbox raw TUN"
        configuration.includeAllNetworks=false
        var body=LBXConfig();body.configJson=AppleDemoConfiguration.content
        var startup=LBXRequest();startup.protocolVersion=1;startup.start=body
        do { configuration.providerConfiguration=["startup_request":try startup.serializedData()] }
        catch { finish(ticket,error);return }
        selected.protocolConfiguration=configuration
        selected.localizedDescription="Libbox raw TUN example"
        selected.isEnabled=true
        selected.saveToPreferences { error in
            self.onMain {
                guard self.generation==ticket else { return }
                if let error { self.finish(ticket,error);return }
                selected.loadFromPreferences { error in
                    self.onMain {
                        guard self.generation==ticket else { return }
                        if let error { self.finish(ticket,error);return }
                        do {
                            try selected.connection.startVPNTunnel()
                            self.busy=false;self.message="Starting raw TUN; query status after connected"
                        } catch { self.finish(ticket,error) }
                    }
                }
            }
        }
    }
    func stop() {
        generation &+= 1;busy=false
        manager?.connection.stopVPNTunnel()
        message="Stopping; pending startup completions will be ignored"
    }
    func status() {
        guard let session=manager?.connection as? NETunnelProviderSession else { message="Start or load the VPN first";return }
        requestID &+= 1
        var request=LBXRequest();request.protocolVersion=1;request.id=requestID
        request.timeoutMs=2000;request.getStatus=LBXEmpty()
        let ticket=generation
        do {
            try session.sendProviderMessage(request.serializedData()) { data in
                self.onMain {
                    guard self.generation==ticket else { return }
                    guard let data else { self.message="No provider response";return }
                    do {
                        let response=try LBXResponse(serializedBytes:data)
                        guard response.protocolVersion==1,response.id==request.id else { self.message="Protocol mismatch";return }
                        self.message=try response.jsonString()
                    } catch { self.message=error.localizedDescription }
                }
            }
        } catch { message=error.localizedDescription }
    }
    private func finish(_ ticket:UInt64,_ error:Error) {
        guard generation==ticket else { return }
        busy=false;message=error.localizedDescription
    }
}

struct HostControllerError:LocalizedError {
    let errorDescription:String?
    init(_ message:String) { errorDescription=message }
}

enum AppleDemoConfiguration {
    static let content="""
    {
      "log":{"level":"warn"},
      "dns":{"servers":[{"type":"https","tag":"dns","server":"1.1.1.1","path":"/dns-query","tls":{"enabled":true,"server_name":"cloudflare-dns.com"}}]},
      "inbounds":[{"type":"tun","tag":"tun","address":["172.19.0.1/30","fdfe:dcba:9876::1/126"],"mtu":1500,"dns_mode":"hijack","auto_route":true}],
      "outbounds":[{"type":"direct","tag":"direct"}],
      "route":{"default_domain_resolver":"dns","rules":[{"protocol":"dns","action":"hijack-dns"}],"final":"direct"}
    }
    """
}

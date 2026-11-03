import Foundation
import NetworkExtension

struct TunnelSettings {
    let mtu: Int
    let addresses: [String]
    let dnsServers: [String]
    let routes: [String]
    let excludeRoutes: [String]
    let includePackages: [String]
    let excludePackages: [String]
    let httpProxy: Proxy?
    struct Proxy {
        let enabled: Bool
        let server: String
        let port: Int
        let bypassDomains: [String]
        let matchDomains: [String]
    }
    init(_ value: LBXTunOptions) throws {
        guard value.includePackages.isEmpty && value.excludePackages.isEmpty else { throw HostError("Per-app routing needs a managed Apple VPN configuration") }
        mtu=Int(value.mtu);addresses=value.addresses;dnsServers=value.dnsServers
        routes=value.routes;excludeRoutes=value.excludeRoutes;includePackages=value.includePackages;excludePackages=value.excludePackages
        if value.hasHTTPProxy { let p=value.httpProxy;httpProxy=Proxy(enabled:p.enabled,server:p.server,port:Int(p.port),bypassDomains:p.bypassDomains,matchDomains:p.matchDomains) } else { httpProxy=nil }
    }
    static func prefix(_ value: String) throws -> (String, Int) {
        let parts = value.split(separator: "/")
        guard parts.count == 2, let count = Int(parts[1]), count >= 0, count <= (parts[0].contains(":") ? 128 : 32) else { throw HostError("Invalid route prefix") }
        return (String(parts[0]), count)
    }
    static func mask(_ bits: Int) -> String {
        let value: UInt32 = bits == 0 ? 0 : UInt32.max << UInt32(32-bits)
        return [24,16,8,0].map { String((value >> $0) & 255) }.joined(separator: ".")
    }
    func networkSettings() throws -> NEPacketTunnelNetworkSettings {
        let result = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
        let parsed = try addresses.map(Self.prefix)
        let v4 = parsed.filter { !$0.0.contains(":") }, v6 = parsed.filter { $0.0.contains(":") }
        if !v4.isEmpty {
            let settings = NEIPv4Settings(addresses: v4.map(\.0), subnetMasks: v4.map { Self.mask($0.1) })
            settings.includedRoutes = try routes.filter { !$0.contains(":") }.map { let value = try Self.prefix($0); return NEIPv4Route(destinationAddress:value.0,subnetMask:Self.mask(value.1)) }
            settings.excludedRoutes = try excludeRoutes.filter { !$0.contains(":") }.map { let value = try Self.prefix($0); return NEIPv4Route(destinationAddress:value.0,subnetMask:Self.mask(value.1)) }
            result.ipv4Settings = settings
        }
        if !v6.isEmpty {
            let settings = NEIPv6Settings(addresses: v6.map(\.0), networkPrefixLengths: v6.map { NSNumber(value:$0.1) })
            settings.includedRoutes = try routes.filter { $0.contains(":") }.map { let value = try Self.prefix($0); return NEIPv6Route(destinationAddress:value.0,networkPrefixLength:NSNumber(value:value.1)) }
            settings.excludedRoutes = try excludeRoutes.filter { $0.contains(":") }.map { let value = try Self.prefix($0); return NEIPv6Route(destinationAddress:value.0,networkPrefixLength:NSNumber(value:value.1)) }
            result.ipv6Settings = settings
        }
        if !dnsServers.isEmpty { let dns = NEDNSSettings(servers:dnsServers); dns.matchDomains = [""]; result.dnsSettings = dns }
        if let proxy = httpProxy, proxy.enabled {
            let settings = NEProxySettings()
            settings.httpEnabled = true; settings.httpsEnabled = true
            settings.httpServer = NEProxyServer(address:proxy.server,port:proxy.port)
            settings.httpsServer = settings.httpServer
            settings.exceptionList = proxy.bypassDomains; settings.matchDomains = proxy.matchDomains
            result.proxySettings = settings
        }
        result.mtu = NSNumber(value:mtu)
        return result
    }
}

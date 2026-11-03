import Foundation
import Network
import Darwin

enum NetworkSnapshot {
    static func message(path: NWPath) throws -> LBXNetwork {
        var result=LBXNetwork();result.defaultInterface = -1
        guard path.status == .satisfied else { return result }
        var interfaces:[String:LBXInterface]=[:]
        var head:UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head)==0 else { throw HostError("getifaddrs failed") }
        defer{freeifaddrs(head)}
        var cursor=head
        while let current=cursor {
            let value=current.pointee;cursor=value.ifa_next
            let name=String(cString:value.ifa_name)
            guard value.ifa_flags & UInt32(IFF_UP) != 0,value.ifa_flags & UInt32(IFF_LOOPBACK) == 0,
                  !name.hasPrefix("utun"),!name.hasPrefix("ipsec"),let address=value.ifa_addr else { continue }
            let index=if_nametoindex(value.ifa_name)
            guard index>0,index<=Int32.max else{continue}
            var item=interfaces[name] ?? LBXInterface()
            item.index=Int32(index);item.name=name;if item.mtu==0{item.mtu=1500};if item.type.isEmpty{item.type="other"}
            if address.pointee.sa_family==UInt8(AF_LINK),let raw=value.ifa_data {
                let mtu=raw.assumingMemoryBound(to:if_data.self).pointee.ifi_mtu
                if mtu>0,mtu<=Int32.max{item.mtu=Int32(mtu)}
            }
            if let mask=value.ifa_netmask,let prefix=prefix(address:address,mask:mask){item.addresses.append(prefix);item.addresses.sort()}
            interfaces[name]=item
        }
        var selected:String?
        for interface in path.availableInterfaces where interfaces[interface.name] != nil {
            switch interface.type { case .wifi:interfaces[interface.name]?.type="wifi";case .cellular:interfaces[interface.name]?.type="cellular";case .wiredEthernet:interfaces[interface.name]?.type="ethernet";default:break }
            if selected==nil && path.usesInterfaceType(interface.type){selected=interface.name}
        }
        guard let name=selected,let selectedInterface=interfaces[name] else{return result}
        interfaces[name]?.metered=path.isExpensive;interfaces[name]?.constrained=path.isConstrained
        result.defaultInterface=selectedInterface.index
        result.interfaces=interfaces.keys.sorted().compactMap{interfaces[$0]}
        // The core config supplies explicit DoH. No invented system DNS servers.
        return result
    }
    private static func prefix(address:UnsafeMutablePointer<sockaddr>,mask:UnsafeMutablePointer<sockaddr>)->String?{
        var host=[CChar](repeating:0,count:Int(NI_MAXHOST))
        guard address.pointee.sa_family==UInt8(AF_INET)||address.pointee.sa_family==UInt8(AF_INET6),
              getnameinfo(address,socklen_t(address.pointee.sa_len),&host,socklen_t(host.count),nil,0,NI_NUMERICHOST)==0 else{return nil}
        let bits:Int
        if address.pointee.sa_family==UInt8(AF_INET){
            bits=mask.withMemoryRebound(to:sockaddr_in.self,capacity:1){pointer in withUnsafeBytes(of:pointer.pointee.sin_addr){$0.reduce(0){$0+$1.nonzeroBitCount}}}
        }else{
            bits=mask.withMemoryRebound(to:sockaddr_in6.self,capacity:1){pointer in withUnsafeBytes(of:pointer.pointee.sin6_addr){$0.reduce(0){$0+$1.nonzeroBitCount}}}
        }
        return "\(String(cString:host).split(separator:"%")[0])/\(bits)"
    }
}

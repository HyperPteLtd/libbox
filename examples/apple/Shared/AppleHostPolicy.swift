import Foundation

enum AppleHostPolicy {
    static func engineOptions(home:URL,includeAllNetworks:Bool)->LBXEngineOptions {
        var options=LBXEngineOptions()
        options.workingDirectory=home.path
        options.networkMode = .host
        options.underNetworkExtension=true
        options.includeAllNetworks=includeAllNetworks
        options.memoryProfile = .auto
        options.eventBuffer=64
        options.hostCapabilities=[.openTun]
        return options
    }
}

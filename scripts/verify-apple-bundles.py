#!/usr/bin/env python3
"""Inspect compiled bundle structure only. Never launch or activate either host."""
from pathlib import Path
import plistlib

ROOT=Path(__file__).resolve().parents[1]
PRODUCTS=ROOT/'dist/examples/apple/Build/Products'

def plist(path):
    return plistlib.loads(path.read_bytes())

ios=PRODUCTS/'Debug-iphoneos/LibboxHost.app'
ios_info=plist(ios/'Info.plist')
ios_extension=ios/'PlugIns/PacketTunnel.appex'
ios_ne=plist(ios_extension/'Info.plist')
assert ios_ne['CFBundleIdentifier']==ios_info['CFBundleIdentifier']+'.tunnel'
assert ios_ne['NSExtension']['NSExtensionPointIdentifier']=='com.apple.networkextension.packet-tunnel'
assert (ios_extension/ios_ne['CFBundleExecutable']).is_file()

mac=PRODUCTS/'Debug/MacHost.app'
mac_info=plist(mac/'Contents/Info.plist')
extensions=list((mac/'Contents/Library/SystemExtensions').glob('*.systemextension'))
assert len(extensions)==1, f'Expected one system extension, got {extensions}'
extension=extensions[0]
mac_ne=plist(extension/'Contents/Info.plist')
identifier=mac_info['CFBundleIdentifier']+'.tunnel'
assert mac_ne['CFBundleIdentifier']==identifier
assert extension.name==identifier+'.systemextension'
assert mac_ne['CFBundlePackageType']=='SYSX'
assert mac_ne['NetworkExtension']['NEMachServiceName'].endswith(identifier)
assert mac_ne['NetworkExtension']['NEProviderClasses']['com.apple.networkextension.packet-tunnel'].endswith('.PacketTunnelProvider')
assert (extension/'Contents/MacOS'/mac_ne['CFBundleExecutable']).is_file()
assert not (PRODUCTS/'Debug/MacHost').exists(), 'Obsolete native CLI product remains'
print('Apple App/NE bundle structure verified; no device, simulator, or VPN launched')

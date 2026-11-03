using Hyper.Libbox.Protocol;
using LibboxHost;
using System.Runtime.InteropServices;

if (args.Length != 1 || (args[0] != "--smoke" && args[0] != "--tun" && args[0] != "--tun-smoke" && !File.Exists(args[0])))
{
    Console.WriteLine("LibboxHost --smoke | --tun | --tun-smoke | /path/to/config.json");
    Console.WriteLine("--smoke does not create TUN or change routes. --tun requires Windows elevation.");
    return 2;
}
bool smoke = args[0] == "--smoke";
bool tunSmoke = args[0] == "--tun-smoke";
bool tun = args[0] == "--tun" || tunSmoke;
if (tun && (!OperatingSystem.IsWindows() || !Elevation.IsAdministrator()))
{
    Console.Error.WriteLine("Run --tun in an elevated Windows terminal.");
    return 2;
}
var home = smoke ? Path.Combine(Path.GetTempPath(), "libbox-smoke-" + Guid.NewGuid())
                 : Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "LibboxHost", "core");
try
{
    using var core = new CoreClient(home);
    Console.WriteLine(core.Call(new Request { GetInfo = new Empty() }).Info);
    var config = smoke ? "{\"log\":{\"disabled\":true},\"outbounds\":[{\"type\":\"direct\",\"tag\":\"direct\"}]}"
        : File.ReadAllText(tun ? Path.Combine(AppContext.BaseDirectory, "tun.json") : args[0]);
    string? adapterName=null;
    if (tunSmoke)
    {
        var document=System.Text.Json.Nodes.JsonNode.Parse(config)!;
        var inbound=document["inbounds"]![0]!;
        adapterName="LibboxTest-"+Guid.NewGuid().ToString("N")[..8];
        inbound["interface_name"]=adapterName;
        inbound["auto_route"]=false; inbound["strict_route"]=false;
        config=document.ToJsonString();
    }
    core.Call(new Request { CheckConfig = new Config { ConfigJson = config } });
    core.Call(new Request { Start = new Config { ConfigJson = config } });
    Console.WriteLine(core.Call(new Request { GetStatus = new Empty() }).Status);
    if (tunSmoke)
    {
        bool found=System.Net.NetworkInformation.NetworkInterface.GetAllNetworkInterfaces().Any(i=>i.Name==adapterName);
        if (!found) throw new IOException("Wintun adapter did not appear");
        Console.WriteLine("Wintun adapter created without default routes: "+adapterName);
    }
    if (!smoke && !tunSmoke)
    {
        Console.WriteLine("Running. Press Ctrl+C to stop and release the adapter/routes.");
        using var cancellation = new CancellationTokenSource();
        Console.CancelKeyPress += (_, e) => { e.Cancel = true; cancellation.Cancel(); };
        try { await Task.Delay(Timeout.Infinite, cancellation.Token); } catch (OperationCanceledException) { }
    }
    core.Call(new Request { Stop = new Empty() });
    Console.WriteLine(core.Call(new Request { GetStatus = new Empty() }).Status);
}
finally
{
    if (smoke && Directory.Exists(home)) Directory.Delete(home, true);
}
return 0;

internal static class Elevation
{
    [DllImport("shell32.dll", EntryPoint = "IsUserAnAdmin")]
    [return: MarshalAs(UnmanagedType.Bool)]
    internal static extern bool IsAdministrator();
}

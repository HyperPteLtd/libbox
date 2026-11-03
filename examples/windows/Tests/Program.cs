using Hyper.Libbox.Protocol;
using LibboxHost;

var original=new Request { Id=9,TimeoutMs=1000,DeadlineUnixMs=1200,UpdateNetwork=new Network { Interfaces={new Interface { Name="original" }} } };
Parallel.For(1,100,i=>{
    var prepared=CoreClient.PrepareRequest(original,(ulong)i,1000);
    if(prepared.Id!=(ulong)i||prepared.DeadlineUnixMs!=1200)throw new Exception("request isolation/deadline failed");
    prepared.UpdateNetwork.Interfaces[0].Name="changed";
});
if(original.Id!=9||original.ProtocolVersion!=0||original.UpdateNetwork.Interfaces[0].Name!="original")throw new Exception("caller request was mutated");
var expired=CoreClient.PrepareRequest(new Request { DeadlineUnixMs=500 },1,1000);
if(expired.DeadlineUnixMs!=500)throw new Exception("expired request gained a fresh deadline");
Console.WriteLine("Managed request concurrency tests passed; no DLL or network loaded");

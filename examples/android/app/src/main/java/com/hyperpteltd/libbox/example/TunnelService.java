package com.hyperpteltd.libbox.example;

import android.app.*;
import android.content.*;
import android.content.pm.ServiceInfo;
import android.net.*;
import android.os.*;
import com.hyperpteltd.libbox.HostMessageHandler;
import com.hyperpteltd.libbox.Libbox;
import com.hyperpteltd.libbox.MessageSession;
import com.hyperpteltd.libbox.protocol.*;
import java.net.NetworkInterface;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;

public final class TunnelService extends VpnService implements HostMessageHandler {
    public static final String STOP = "stop";
    private final ExecutorService operations = Executors.newSingleThreadExecutor();
    private final ExecutorService shutdown = Executors.newSingleThreadExecutor();
    private final ExecutorService networks = Executors.newSingleThreadExecutor();
    private final AtomicBoolean networkQueued = new AtomicBoolean();
    private final AtomicReference<MessageSession> endpoint = new AtomicReference<>();
    private final AtomicBoolean stopping = new AtomicBoolean(true);
    private final AtomicLong sequence = new AtomicLong();
    private final Object lifecycle = new Object(), tunLock = new Object();
    private int phase, serviceStartId;
    private long generation;
    private boolean destroyed, monitoring;
    private final OwnedTun<ParcelFileDescriptor> tun=new OwnedTun<>();
    private ConnectivityManager connectivity;
    private final ConnectivityManager.NetworkCallback callback = new ConnectivityManager.NetworkCallback() {
        @Override public void onAvailable(android.net.Network network) { refresh(); }
        @Override public void onLost(android.net.Network network) { refresh(); }
        @Override public void onLinkPropertiesChanged(android.net.Network network, LinkProperties properties) { refresh(); }
        @Override public void onCapabilitiesChanged(android.net.Network network, NetworkCapabilities caps) { refresh(); }
    };
    public final class LocalBinder extends Binder {
        public byte[] handle(byte[] data) throws Exception {
            if (data.length>4*1024*1024) throw new IllegalArgumentException("Message too large");
            Request request=Request.parseFrom(data);
            switch(request.getMethodCase()) {
                case CREATE_ENGINE: case DESTROY_ENGINE: case START: case RELOAD: case STOP: case UPDATE_NETWORK:
                    return failure(request.getId(),"unsupported","Use the service lifecycle");
                default: break;
            }
            MessageSession session=endpoint.get();
            return session==null?failure(request.getId(),"not_running","VPN is not running"):session.handleMessage(data);
        }
    }
    private static byte[] failure(long id,String code,String message) {
        return Response.newBuilder().setProtocolVersion(1).setId(id).setError(com.hyperpteltd.libbox.protocol.Error.newBuilder().setCode(code).setMessage(message)).build().toByteArray();
    }
    private Response call(MessageSession session,Request.Builder request) throws Exception {
        long id=sequence.incrementAndGet();
        request.setProtocolVersion(1).setId(id).setTimeoutMs(30000).setDeadlineUnixMs(System.currentTimeMillis()+30000);
        Response response=Response.parseFrom(session.handleMessage(request.build().toByteArray()));
        if(response.getProtocolVersion()!=1||response.getId()!=id)throw new Exception("Protocol mismatch");
        if(response.hasError())throw new Exception(response.getError().getCode()+": "+response.getError().getMessage());
        return response;
    }
    @Override public IBinder onBind(Intent intent) {
        return SERVICE_INTERFACE.equals(intent.getAction())?super.onBind(intent):new LocalBinder();
    }
    @Override public void onCreate() {
        super.onCreate();go.Seq.setContext(getApplicationContext());
        connectivity=getSystemService(ConnectivityManager.class);
    }
    @Override public int onStartCommand(Intent intent,int flags,int startId) {
        synchronized(lifecycle){serviceStartId=startId;}
        if(intent==null||STOP.equals(intent.getAction())){stopCore();return START_NOT_STICKY;}
        if(VpnService.prepare(this)!=null){stopSelf(startId);return START_NOT_STICKY;}
        final long ticket;
        synchronized(lifecycle){if(phase!=0||destroyed)return START_NOT_STICKY;phase=1;ticket=++generation;stopping.set(false);}
        try {
            NotificationManager notifications=getSystemService(NotificationManager.class);
            if(Build.VERSION.SDK_INT>=26)notifications.createNotificationChannel(new NotificationChannel("vpn","VPN",NotificationManager.IMPORTANCE_LOW));
            Notification.Builder builder=Build.VERSION.SDK_INT>=26?new Notification.Builder(this,"vpn"):new Notification.Builder(this);
            Notification notification=builder.setSmallIcon(android.R.drawable.stat_sys_warning).setContentTitle("Libbox VPN").setContentText("Stop from the app to disconnect").build();
            if(Build.VERSION.SDK_INT>=34)startForeground(1,notification,ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED);else startForeground(1,notification);
            connectivity.registerNetworkCallback(new NetworkRequest.Builder().addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET).addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN).build(),callback);
            monitoring=true;
        }catch(Exception error){android.util.Log.e("LibboxHost","Host setup failed",error);stopCore();return START_NOT_STICKY;}
        operations.execute(()->{
            MessageSession opened=Libbox.newMessageSession(this);
            try {
                synchronized(lifecycle){if(generation!=ticket||phase!=1||destroyed)throw new Exception("Startup canceled");endpoint.set(opened);}
                EngineOptions options=EngineOptions.newBuilder()
                    .setWorkingDirectory(new java.io.File(getFilesDir(),"core").getAbsolutePath())
                    .setNetworkMode(EngineOptions.NetworkMode.HOST).setEventBuffer(64)
                    .addHostCapabilities(EngineOptions.HostCapability.OPEN_TUN)
                    .addHostCapabilities(EngineOptions.HostCapability.PROTECT_SOCKET).build();
                call(opened,Request.newBuilder().setCreateEngine(options));
                networks.submit(()->{pushNetwork(ticket,opened);return null;}).get(5,TimeUnit.SECONDS);
                if(stopping.get())throw new Exception("Startup canceled");
                call(opened,Request.newBuilder().setStart(Config.newBuilder().setConfigJson(CONFIG)));
                synchronized(lifecycle){if(generation!=ticket||phase!=1||destroyed)throw new Exception("Startup canceled");phase=2;}
            }catch(Exception error){
                android.util.Log.e("LibboxHost","VPN startup failed",error);
                try{opened.close();}catch(Exception ignored){}
                endpoint.compareAndSet(opened,null);
                synchronized(lifecycle){if(generation==ticket)stopCore();}
            }
        });
        return START_NOT_STICKY;
    }
    private void refresh() {
        if(stopping.get()||!networkQueued.compareAndSet(false,true))return;
        final long ticket;synchronized(lifecycle){ticket=generation;}
        try{networks.execute(()->{networkQueued.set(false);MessageSession session=endpoint.get();if(session!=null)try{pushNetwork(ticket,session);}catch(Exception error){if(!stopping.get())android.util.Log.w("LibboxHost","Network update",error);}});}
        catch(RejectedExecutionException ignored){networkQueued.set(false);}
    }
    private void pushNetwork(long ticket,MessageSession session) throws Exception {
        synchronized(lifecycle){if(generation!=ticket||stopping.get()||endpoint.get()!=session)return;}
        var value=snapshot();
        synchronized(lifecycle){if(generation!=ticket||stopping.get()||endpoint.get()!=session)return;}
        call(session,Request.newBuilder().setUpdateNetwork(value));
    }
    private com.hyperpteltd.libbox.protocol.Network snapshot() throws Exception {
        List<android.net.Network> physical=new ArrayList<>();
        for(android.net.Network network:connectivity.getAllNetworks()){
            NetworkCapabilities caps=connectivity.getNetworkCapabilities(network);
            if(caps!=null&&!caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)&&caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET))physical.add(network);
        }
        android.net.Network active=connectivity.getActiveNetwork();
        physical.sort(Comparator.comparingInt(network->network.equals(active)?0:1));
        com.hyperpteltd.libbox.protocol.Network.Builder result=com.hyperpteltd.libbox.protocol.Network.newBuilder().setDefaultInterface(-1);
        Set<Integer> seen=new HashSet<>();android.net.Network selected=null;
        for(android.net.Network network:physical){
            LinkProperties properties=connectivity.getLinkProperties(network);NetworkCapabilities caps=connectivity.getNetworkCapabilities(network);
            if(properties==null||caps==null||properties.getInterfaceName()==null)continue;
            NetworkInterface iface=NetworkInterface.getByName(properties.getInterfaceName());
            if(iface==null||iface.getIndex()<=0||!seen.add(iface.getIndex()))continue;
            int mtu=properties.getMtu()>0?properties.getMtu():iface.getMTU();
            Interface.Builder value=Interface.newBuilder().setIndex(iface.getIndex()).setName(iface.getName()).setMtu(mtu>0?mtu:1500)
                .setType(caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)?"wifi":caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR)?"cellular":caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)?"ethernet":"other")
                .setMetered(!caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED));
            for(LinkAddress address:properties.getLinkAddresses())value.addAddresses(address.getAddress().getHostAddress().split("%")[0]+"/"+address.getPrefixLength());
            for(java.net.InetAddress address:properties.getDnsServers())value.addDnsServers(address.getHostAddress());
            result.addInterfaces(value);
            if(selected==null){selected=network;result.setDefaultInterface(iface.getIndex());}
        }
        setUnderlyingNetworks(selected==null?new android.net.Network[0]:new android.net.Network[]{selected});
        return result.build();
    }
    // All reverse calls also use protobuf; no JSON or per-operation FFI callback.
    @Override public byte[] handleHostMessage(byte[] data) throws Exception {
        if(data.length>4*1024*1024)throw new Exception("Host message too large");
        HostRequest request=HostRequest.parseFrom(data);
        HostResponse.Builder response=HostResponse.newBuilder().setProtocolVersion(1).setId(request.getId());
        try {
            if(request.getProtocolVersion()!=1)throw new Exception("Unsupported host protocol");
            if(stopping.get())throw new Exception("VPN is stopping");
            switch(request.getMethodCase()){
                case OPEN_TUN:response.setTun(TunDescriptor.newBuilder().setFd(openTun(request.getOpenTun())));break;
                case PROTECT_SOCKET:
                    if(!protect(request.getProtectSocket().getFd()))throw new Exception("Socket protection failed");
                    response.setEmpty(Empty.getDefaultInstance());break;
                default:throw new Exception("Unsupported host capability");
            }
        }catch(Exception error){response.setError(com.hyperpteltd.libbox.protocol.Error.newBuilder().setCode("host_error").setMessage(error.toString()));}
        return response.build().toByteArray();
    }
    private int openTun(TunOptions options) throws Exception {
        Builder builder=new Builder().setSession("Libbox").setMtu(options.getMtu());
        for(String address:options.getAddressesList()){String[] prefix=address.split("/");builder.addAddress(prefix[0],Integer.parseInt(prefix[1]));}
        for(String route:options.getRoutesList()){String[] prefix=route.split("/");builder.addRoute(prefix[0],Integer.parseInt(prefix[1]));}
        for(String dns:options.getDnsServersList())builder.addDnsServer(dns);
        if(options.getIncludePackagesCount()>0&&options.getExcludePackagesCount()>0)throw new Exception("Android cannot combine allow/deny application lists");
        for(String name:options.getIncludePackagesList())builder.addAllowedApplication(name);
        for(String name:options.getExcludePackagesList())builder.addDisallowedApplication(name);
        if(options.hasHttpProxy()&&options.getHttpProxy().getEnabled()){
            HTTPProxy proxy=options.getHttpProxy();
            if(Build.VERSION.SDK_INT<29||proxy.getMatchDomainsCount()>0)throw new Exception("Unsupported HTTP proxy options");
            builder.setHttpProxy(ProxyInfo.buildDirectProxy(proxy.getServer(),proxy.getPort(),proxy.getBypassDomainsList()));
        }
        ParcelFileDescriptor opened=builder.establish();if(opened==null)throw new Exception("VPN permission was revoked");
        synchronized(tunLock){if(stopping.get()){opened.close();throw new Exception("VPN is stopping");}tun.replace(opened);return opened.getFd();}
    }
    private void stopCore() {
        final MessageSession current;final int stopId;
        synchronized(lifecycle){
            if(phase==3)return;
            if(phase==0){if(destroyed){operations.shutdownNow();networks.shutdownNow();shutdown.shutdown();}stopForeground(true);stopSelf(serviceStartId);return;}
            phase=3;++generation;stopping.set(true);current=endpoint.getAndSet(null);stopId=serviceStartId;
        }
        if(monitoring){monitoring=false;try{connectivity.unregisterNetworkCallback(callback);}catch(Exception ignored){}}
        shutdown.execute(()->{
            if(current!=null){
                try{call(current,Request.newBuilder().setDestroyEngine(Empty.getDefaultInstance()));}catch(Exception error){android.util.Log.w("LibboxHost","Core shutdown",error);}
                finally{try{current.close();}catch(Exception ignored){}}
            }
            synchronized(tunLock){try{tun.close();}catch(Exception error){android.util.Log.w("LibboxHost","TUN close",error);}}
            // Drain snapshot work before resetting the underlying network or
            // allowing another start. Old tasks must not mutate the next VPN.
            try{networks.submit(()->setUnderlyingNetworks(null)).get();}
            catch(Exception error){android.util.Log.w("LibboxHost","Network shutdown",error);}
            synchronized(lifecycle){stopForeground(true);stopSelf(stopId);phase=0;if(destroyed){operations.shutdownNow();networks.shutdownNow();shutdown.shutdown();}}
        });
    }
    @Override public void onRevoke(){stopCore();}
    @Override public void onDestroy(){synchronized(lifecycle){destroyed=true;}stopCore();super.onDestroy();}
    @Override public void onTrimMemory(int level){
        super.onTrimMemory(level);if(level<TRIM_MEMORY_RUNNING_LOW||stopping.get())return;
        try{operations.execute(()->{MessageSession session=endpoint.get();if(session!=null)try{call(session,Request.newBuilder().setMemoryPressure(MemoryPressure.newBuilder().setLevel(level>=TRIM_MEMORY_RUNNING_CRITICAL?MemoryPressure.Level.CRITICAL:MemoryPressure.Level.WARNING)));}catch(Exception ignored){}});}catch(RejectedExecutionException ignored){}
    }
    private static final String CONFIG="""
      {"log":{"level":"warn"},"dns":{"servers":[{"type":"https","tag":"dns","server":"1.1.1.1","path":"/dns-query","tls":{"enabled":true,"server_name":"cloudflare-dns.com"}}]},
      "inbounds":[{"type":"tun","tag":"tun","address":["172.19.0.1/30","fdfe:dcba:9876::1/126"],"mtu":1500,"dns_mode":"hijack","auto_route":true}],
      "outbounds":[{"type":"direct","tag":"direct"}],"route":{"default_domain_resolver":"dns","rules":[{"protocol":"dns","action":"hijack-dns"}],"final":"direct"}}
      """;
}

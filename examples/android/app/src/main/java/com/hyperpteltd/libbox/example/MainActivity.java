package com.hyperpteltd.libbox.example;

import android.app.Activity;
import android.content.*;
import android.net.VpnService;
import android.os.*;
import android.widget.*;
import com.hyperpteltd.libbox.protocol.*;

public final class MainActivity extends Activity {
    private TextView status;
    private TunnelService.LocalBinder binder;
    private boolean bound;
    private final ServiceConnection connection = new ServiceConnection() {
        public void onServiceConnected(ComponentName name, IBinder service) { binder = (TunnelService.LocalBinder)service; }
        public void onServiceDisconnected(ComponentName name) { binder = null; }
    };
    @Override public void onCreate(Bundle saved) {
        super.onCreate(saved);
        LinearLayout layout = new LinearLayout(this); layout.setOrientation(LinearLayout.VERTICAL); layout.setPadding(32,64,32,32);
        status = new TextView(this); status.setText("Libbox host example. Start requests system VPN permission."); layout.addView(status);
        Button start = new Button(this); start.setText("Start VPN"); layout.addView(start);
        start.setOnClickListener(v -> {
            Intent permission = VpnService.prepare(this);
            if (permission != null) startActivityForResult(permission,1); else startTunnel();
        });
        Button query = new Button(this); query.setText("Protobuf status"); layout.addView(query);
        query.setOnClickListener(v -> {
            if (binder == null) { status.setText("Service not bound yet"); return; }
            try {
                Request request = Request.newBuilder().setProtocolVersion(1).setId(1).setTimeoutMs(2000).setGetStatus(Empty.getDefaultInstance()).build();
                Response reply = Response.parseFrom(binder.handle(request.toByteArray()));
                if (reply.getProtocolVersion()!=1 || reply.getId()!=request.getId()) throw new Exception("Protocol mismatch");
                status.setText(reply.hasError() ? reply.getError().getCode()+": "+reply.getError().getMessage() : reply.getStatus().getState());
            } catch (Exception error) { status.setText(error.toString()); }
        });
        Button stop = new Button(this); stop.setText("Stop VPN"); layout.addView(stop);
        stop.setOnClickListener(v -> startService(new Intent(this,TunnelService.class).setAction(TunnelService.STOP)));
        setContentView(layout);
        bound = bindService(new Intent(this,TunnelService.class).setAction("bind-local"),connection,BIND_AUTO_CREATE);
        if (Build.VERSION.SDK_INT>=33) requestPermissions(new String[]{android.Manifest.permission.POST_NOTIFICATIONS},2);
    }
    private void startTunnel() {
        Intent intent = new Intent(this,TunnelService.class).setAction("start");
        if (Build.VERSION.SDK_INT>=26) startForegroundService(intent); else startService(intent);
    }
    @Override protected void onActivityResult(int request,int result,Intent data) {
        super.onActivityResult(request,result,data);
        if (request==1 && result==RESULT_OK) startTunnel();
    }
    @Override protected void onDestroy() { if (bound) unbindService(connection); super.onDestroy(); }
}

package com.hyperpteltd.libbox.example;

import java.io.Closeable;
import java.io.IOException;

public final class OwnedTunTest {
    static final class FakeFD implements Closeable {
        int closes;final boolean fails;
        FakeFD(boolean fails){this.fails=fails;}
        public void close()throws IOException{closes++;if(fails)throw new IOException("close failed");}
    }
    public static void main(String[]args)throws Exception{
        OwnedTun<FakeFD> owner=new OwnedTun<>();FakeFD first=new FakeFD(true),second=new FakeFD(false);
        owner.replace(first);
        try{owner.replace(second);throw new AssertionError("missing close error");}catch(IOException expected){}
        owner.close();owner.close();
        if(first.closes!=1||second.closes!=1)throw new AssertionError("descriptor leak or duplicate close");
        System.out.println("Android FD ownership unit test passed; no Android runtime/device");
    }
}

package com.hyperpteltd.libbox.example;

import java.io.Closeable;
import java.io.IOException;

// Owns only the host's original descriptor. The SDK must drain its duplicate
// before close is called. Adopt first so a failure closing the previous value
// cannot leak the newly established interface.
final class OwnedTun<T extends Closeable> implements Closeable {
    private T current;
    synchronized void replace(T next) throws IOException {
        T previous=current;current=next;
        if(previous!=null)previous.close();
    }
    public synchronized void close() throws IOException {
        T previous=current;current=null;
        if(previous!=null)previous.close();
    }
}

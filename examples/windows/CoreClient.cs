using System.Runtime.InteropServices;
using Google.Protobuf;
using Hyper.Libbox.Protocol;

namespace LibboxHost;

internal static class Native
{
    private static readonly Lazy<IntPtr> Library = new(() => {
        var configured=Environment.GetEnvironmentVariable("LIBBOX_NATIVE_PATH");
        var filename=OperatingSystem.IsWindows()?"libbox.dll":OperatingSystem.IsMacOS()?"libbox.dylib":"libbox.so";
        return NativeLibrary.Load(Path.GetFullPath(configured??Path.Combine(AppContext.BaseDirectory,filename)));
    },LazyThreadSafetyMode.ExecutionAndPublication);
    static Native()
    {
        NativeLibrary.SetDllImportResolver(typeof(Native).Assembly, (name, assembly, path) =>
        {
            if (name != "libbox") return IntPtr.Zero;
            return Library.Value;
        });
    }
    [DllImport("libbox", CallingConvention = CallingConvention.Cdecl)] internal static extern ulong libbox_session_new(IntPtr context, IntPtr callback);
    [DllImport("libbox", CallingConvention = CallingConvention.Cdecl)] internal static extern IntPtr libbox_session_call(ulong session, byte[] data, ulong length, out ulong responseLength);
    [DllImport("libbox", CallingConvention = CallingConvention.Cdecl)] internal static extern void libbox_session_free(ulong session);
    [DllImport("libbox", CallingConvention = CallingConvention.Cdecl)] internal static extern void libbox_free_buffer(IntPtr value);
}

// The only application protocol is protobuf, including initialization and teardown.
// Native methods allocate/free an opaque transport endpoint, not the engine.
public sealed class CoreClient : IDisposable
{
    private long session;
    private long sequence;
    private readonly object disposal=new();
    public CoreClient(string home)
    {
        session = checked((long)Native.libbox_session_new(IntPtr.Zero, IntPtr.Zero));
        try
        {
            Call(new Request { CreateEngine = new EngineOptions {
                WorkingDirectory = Path.GetFullPath(home),
                NetworkMode = EngineOptions.Types.NetworkMode.Native
            } });
        }
        catch { Native.libbox_session_free((ulong)Interlocked.Exchange(ref session, 0)); throw; }
    }
    public Response Call(Request request)
    {
        var current = Interlocked.Read(ref session);
        ObjectDisposedException.ThrowIf(current == 0, this);
        return CallHandle((ulong)current, request);
    }
    private Response CallHandle(ulong current, Request request)
    {
        request=PrepareRequest(request,checked((ulong)Interlocked.Increment(ref sequence)),DateTimeOffset.UtcNow.ToUnixTimeMilliseconds());
        var encoded = request.ToByteArray();
        var pointer = Native.libbox_session_call(current, encoded, (ulong)encoded.Length, out var length);
        if (pointer == IntPtr.Zero) throw new IOException("SDK returned a null response");
        try
        {
            if (length > 4 * 1024 * 1024) throw new IOException("SDK response exceeds the limit");
            var buffer = new byte[(int)length]; Marshal.Copy(pointer, buffer, 0, buffer.Length);
            var response = Response.Parser.ParseFrom(buffer);
            if (response.ProtocolVersion != 1 || response.Id != request.Id) throw new IOException("Response version/id mismatch");
            if (response.Error is not null) throw new IOException($"libbox:{response.Error.Code}: {response.Error.Message}");
            return response;
        }
        finally { Native.libbox_free_buffer(pointer); }
    }
    internal static Request PrepareRequest(Request input,ulong id,long now)
    {
        ArgumentNullException.ThrowIfNull(input);
        var request=input.Clone();
        request.ProtocolVersion=1;request.Id=id;
        if(request.TimeoutMs==0)request.TimeoutMs=30_000;
        var deadline=now+request.TimeoutMs;
        if(request.DeadlineUnixMs<=0||deadline<request.DeadlineUnixMs)request.DeadlineUnixMs=deadline;
        return request;
    }
    public void Dispose()
    {
        lock(disposal)
        {
            var current = Interlocked.Exchange(ref session, 0);
            if (current == 0) return;
            try { CallHandle((ulong)current, new Request { DestroyEngine = new Empty() }); }
            finally { Native.libbox_session_free((ulong)current); }
        }
    }
}

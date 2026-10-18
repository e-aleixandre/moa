package com.ealeixandre.moa.app;

import android.content.Context;
import java.util.concurrent.ScheduledThreadPoolExecutor;
import java.util.concurrent.ThreadFactory;

// The process's one native call: its manager, its thread and its bridge,
// built the first time anything needs them. Services and the Telecom
// connection service reach it from here because the system, not the app,
// creates them.
final class NativeCalls {

    private static NativeCalls shared;

    private final ScheduledThreadPoolExecutor thread;
    private final AndroidCallHost host;
    private final NativeCallManager manager;
    private final NativeCallBridge bridge;

    static synchronized NativeCalls get(Context context) {
        if (shared == null) shared = new NativeCalls(context.getApplicationContext());
        return shared;
    }

    static CallSystem system(Context context) {
        return get(context).host.system();
    }

    // The app is no longer paired with the server a running call belongs to.
    // Nothing is built when no call was ever made.
    static void bindingChanged() {
        NativeCalls calls;
        synchronized (NativeCalls.class) {
            calls = shared;
        }
        if (calls != null) calls.host.post(calls.manager::bindingChanged);
    }

    // The ongoing-call notification's Hang up.
    static void requestHangUp() {
        NativeCalls calls;
        synchronized (NativeCalls.class) {
            calls = shared;
        }
        if (calls != null) calls.host.system().requestHangUp();
    }

    private NativeCalls(Context context) {
        ThreadFactory threads = task -> {
            Thread t = new Thread(task, "moa-call");
            t.setDaemon(true);
            return t;
        };
        thread = new ScheduledThreadPoolExecutor(1, threads);
        thread.setRemoveOnCancelPolicy(true);
        host = new AndroidCallHost(context, DeviceCredentialStore.get(context), thread);
        manager = new NativeCallManager(host);
        bridge = new NativeCallBridge(manager, thread);
        host.setBridge(bridge);
    }

    NativeCallBridge bridge() {
        return bridge;
    }

    void setMicRequester(AndroidCallHost.MicRequester requester) {
        host.setMicRequester(requester);
    }
}

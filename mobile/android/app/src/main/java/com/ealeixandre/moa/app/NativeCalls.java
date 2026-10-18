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
    private final IncomingCalls incoming;

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

    // A call envelope that opened: ring the phone. Origin and authorization
    // are those of the secret that opened it. Any thread.
    static void ring(Context context, CallInvite invite, String origin, String authorization) {
        NativeCalls calls = get(context);
        calls.host.post(() -> calls.incoming.receive(invite, origin, authorization));
    }

    // Answer and Decline, from the notification, the call screen or Telecom.
    static void answer(Context context, String invite) {
        NativeCalls calls = get(context);
        calls.host.post(() -> calls.incoming.answer(invite));
    }

    static void decline(Context context, String invite) {
        NativeCalls calls = get(context);
        calls.host.post(() -> calls.incoming.decline(invite));
    }

    // The invitation ringing now, or null. Builds nothing when none ever rang.
    static String ringingInvite() {
        NativeCalls calls;
        synchronized (NativeCalls.class) {
            calls = shared;
        }
        return calls == null ? null : calls.incoming.ringingInvite();
    }

    // The secret of the incoming-call destination is gone: nothing ringing
    // may be answered with it.
    static void forgetRinging() {
        NativeCalls calls;
        synchronized (NativeCalls.class) {
            calls = shared;
        }
        if (calls != null) calls.host.post(calls.incoming::forgetLocal);
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
        incoming = new IncomingCalls(new AndroidIncomingHost(context, host, manager));
        host.system().setRingingListener(new CallSystem.RingingListener() {
            @Override
            public void answer(String invite) {
                incoming.answer(invite);
            }

            @Override
            public void decline(String invite) {
                incoming.decline(invite);
            }
        });
    }

    NativeCallBridge bridge() {
        return bridge;
    }

    void setMicRequester(AndroidCallHost.MicRequester requester) {
        host.setMicRequester(requester);
    }
}

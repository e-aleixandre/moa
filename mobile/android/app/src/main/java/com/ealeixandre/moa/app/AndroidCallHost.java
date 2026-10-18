package com.ealeixandre.moa.app;

import android.Manifest;
import android.content.Context;
import android.content.pm.PackageManager;
import androidx.core.content.ContextCompat;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;
import okhttp3.OkHttpClient;

// Every platform edge of the call, for NativeCallManager. One thread, "the
// call thread", runs the manager and everything it is called back from:
// OkHttp's, the audio threads', Telecom's and the page's callbacks are all
// moved onto it here, so the manager needs no locks.
final class AndroidCallHost implements NativeCallManager.Host {

    interface MicRequester {
        // Asks for RECORD_AUDIO on screen; answers on any thread.
        void request(java.util.function.Consumer<Boolean> answer);
    }

    private final Context context;
    private final DeviceCredentialStore credentials;
    private final ScheduledExecutorService thread;
    private final OkHttpClient client = CallSocket.newClient();
    private final CallSystem system;
    private final InviteApi invites;
    private volatile NativeCallBridge bridge;
    private volatile MicRequester micRequester;
    private volatile boolean micDenied;

    AndroidCallHost(Context context, DeviceCredentialStore credentials, ScheduledExecutorService thread) {
        this.context = context.getApplicationContext();
        this.credentials = credentials;
        this.thread = thread;
        this.system = new CallSystem(this.context, this::post, this);
        this.invites = new InviteApi(InviteApi.newClient(), this::post);
    }

    InviteApi invites() {
        return invites;
    }

    void setBridge(NativeCallBridge bridge) {
        this.bridge = bridge;
    }

    void setMicRequester(MicRequester requester) {
        micRequester = requester;
    }

    CallSystem system() {
        return system;
    }

    ScheduledExecutorService thread() {
        return thread;
    }

    void post(Runnable task) {
        try {
            thread.execute(task);
        } catch (RejectedExecutionException ignored) {
            // Shutting down.
        }
    }

    @Override
    public String pairedOrigin() {
        return credentials.pairedOrigin();
    }

    @Override
    public String credential(String origin) {
        try {
            DeviceCredentialStore.Stored stored = credentials.load();
            if (stored == null || !stored.origin.equals(origin) || stored.expiresAtMillis <= System.currentTimeMillis()) {
                return null;
            }
            return stored.credential;
        } catch (DeviceCredentialStore.StoreException e) {
            return null;
        }
    }

    @Override
    public NativeCallManager.Socket connect(String url, String authorization, NativeCallManager.SocketEvents events) {
        return CallSocket.open(client, url, authorization, new NativeCallManager.SocketEvents() {
            @Override
            public void opened() {
                post(events::opened);
            }

            @Override
            public void text(String text) {
                post(() -> events.text(text));
            }

            @Override
            public void binary(byte[] data) {
                post(() -> events.binary(data));
            }

            @Override
            public void closed(int code) {
                post(() -> events.closed(code));
            }

            @Override
            public void failed(String code) {
                post(() -> events.failed(code));
            }
        }, thread);
    }

    @Override
    public NativeCallManager.Audio createAudio(NativeCallManager.AudioEvents events) {
        return new CallAudio(new NativeCallManager.AudioEvents() {
            @Override
            public void frame(byte[] frame) {
                events.frame(frame);
            }

            @Override
            public void playback(String type) {
                post(() -> events.playback(type));
            }

            @Override
            public void failed() {
                post(events::failed);
            }
        });
    }

    @Override
    public NativeCallManager.SystemCall systemCall() {
        return system;
    }

    @Override
    public void hangupCall(String origin, String callId, String authorization) {
        invites.post(origin, authorization, "calls/" + callId + "/hangup", (status, body) -> { });
    }

    @Override
    public String micState() {
        if (ContextCompat.checkSelfPermission(context, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
            return "granted";
        }
        return micDenied ? "denied" : "undetermined";
    }

    @Override
    public void requestMic(NativeCallManager.MicResult result) {
        if (micState().equals("granted")) {
            result.granted(true);
            return;
        }
        MicRequester requester = micRequester;
        if (requester == null) {
            micDenied = true;
            result.granted(false);
            return;
        }
        requester.request(granted -> {
            if (!granted) micDenied = true;
            post(() -> result.granted(granted));
        });
    }

    @Override
    public void emit(String id, String type, Object data) {
        NativeCallBridge target = bridge;
        if (target != null) target.deliver(id, type, data);
    }

    @Override
    public Runnable schedule(Runnable task, long delayMillis) {
        try {
            ScheduledFuture<?> future = thread.schedule(task, delayMillis, TimeUnit.MILLISECONDS);
            return () -> future.cancel(false);
        } catch (RejectedExecutionException e) {
            return () -> { };
        }
    }
}

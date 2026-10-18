package com.ealeixandre.moa.app;

import android.os.Handler;
import android.os.Looper;
import androidx.webkit.JavaScriptReplyProxy;
import java.util.Map;
import java.util.concurrent.Executor;
import org.json.JSONException;
import org.json.JSONObject;

// window.MoaNativeCall, the Android side of iOS NativeCallMessageHandler:
// the page's calls arrive as WebMessages from the paired origin's main frame
// and go to the one NativeCallManager; what the call does comes back as
// events on the port of the page that last spoke. Only the paired server's
// page may call it (OriginPolicy "call"), however the listener was scoped.
final class NativeCallBridge {

    private final Executor callThread;
    private final NativeCallManager manager;
    private final Handler main = new Handler(Looper.getMainLooper());
    // The page that last called: events go to it, and to nobody once it is gone.
    private volatile JavaScriptReplyProxy page;

    NativeCallBridge(NativeCallManager manager, Executor callThread) {
        this.manager = manager;
        this.callThread = callThread;
    }

    void onMessage(String raw, OriginPolicy.Caller caller, JavaScriptReplyProxy proxy) {
        NativeCallMessage message = NativeCallMessage.parse(raw);
        if (message == null || !OriginPolicy.mayCall("call", message.op, caller)) return;
        page = proxy;
        callThread.execute(() -> message.applyTo(manager));
    }

    // From the call thread. A page that has gone away is not an error.
    void deliver(String id, String type, Object data) {
        JavaScriptReplyProxy target = page;
        if (target == null) return;
        JSONObject event = new JSONObject();
        try {
            event.put("id", id).put("type", type);
            if (data != null) event.put("data", data instanceof Map ? new JSONObject((Map<?, ?>) data) : data);
        } catch (JSONException e) {
            return;
        }
        String json = event.toString();
        main.post(() -> {
            try {
                target.postMessage(json);
            } catch (RuntimeException ignored) {
                // The page that asked is gone.
            }
        });
    }
}

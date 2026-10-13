package com.ealeixandre.moa.app;

import android.content.Context;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.json.JSONObject;

// window.MoaNativePush for the paired frontend's notifications switch (iOS
// NativePushBridge.swift). Only the main frame of the paired origin reaches
// it (WebBridges + OriginPolicy). It accepts no keys, URLs, destinations or
// credentials and returns none: status is permission, enabled/registered and
// the server's last delivery result.
final class NativePushBridge implements WebBridges.MethodHandler {

    private final Context context;
    // enable may wait minutes for the relay's challenge; status stays answerable.
    private final ExecutorService worker = Executors.newCachedThreadPool();

    NativePushBridge(Context context) {
        this.context = context.getApplicationContext();
    }

    @Override
    public void handle(String method, JSONObject options, OriginPolicy.Caller caller, WebBridges.Reply reply) {
        NativePushRegistration registration = NativePushRegistration.get(context);
        if ("setVisibleSession".equals(method)) {
            PushNavigator.get().setVisibleSession(options.isNull("session") ? null : options.optString("session", null));
            reply.ok(null);
            return;
        }
        if ("openFallback".equals(method)) {
            PushNavigator.get().openFallback(options.optInt("token", 0));
            reply.ok(null);
            return;
        }
        worker.execute(() -> {
            try {
                switch (method) {
                    case "status":
                        reply.ok(registration.status());
                        return;
                    case "enable":
                        registration.enable();
                        reply.ok(registration.status());
                        return;
                    case "disable":
                        registration.disable();
                        reply.ok(registration.status());
                        return;
                    default:
                        reply.fail("unsupported");
                }
            } catch (NativePushRegistration.PushException e) {
                reply.fail(e.code);
            } catch (RuntimeException e) {
                reply.fail("unavailable");
            }
        });
    }
}

package com.ealeixandre.moa.app;

import android.net.Uri;
import android.os.Handler;
import android.os.Looper;
import android.util.Log;
import android.webkit.WebView;
import androidx.webkit.JavaScriptReplyProxy;
import androidx.webkit.ScriptHandler;
import androidx.webkit.WebMessageCompat;
import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import org.json.JSONException;
import org.json.JSONObject;

// Installs window.MoaNativeAuth, window.MoaNativePush and window.MoaShareInbox
// with the same JS API as the iOS container. Each is a document-start script plus a WebMessage
// listener, both restricted by origin rules to the pages allowed to use them:
// the local pairing page, and the paired server's exact origin. A page from
// any other origin gets neither the object nor the port. Never
// addJavascriptInterface: that object would be visible to every origin and
// frame. If the WebView lacks WEB_MESSAGE_LISTENER or DOCUMENT_START_SCRIPT
// nothing is installed, and pairing fails with "unavailable" (fail closed).
//
// The origin rules are the outer fence; every message is checked again
// against OriginPolicy (caller origin, main frame, method), so a stale rule
// left from a previous pairing grants nothing.
final class WebBridges {

    interface MethodHandler {
        // Runs on the UI thread. Reply with reply.ok/fail exactly once.
        void handle(String method, JSONObject options, OriginPolicy.Caller caller, Reply reply);
    }

    static final class Reply {
        private final JavaScriptReplyProxy proxy;
        private final String id;
        private final Handler main = new Handler(Looper.getMainLooper());

        Reply(JavaScriptReplyProxy proxy, String id) {
            this.proxy = proxy;
            this.id = id;
        }

        void ok(Object value) {
            JSONObject message = new JSONObject();
            try {
                message.put("id", id).put("ok", true).put("value", value == null ? new JSONObject() : value);
            } catch (JSONException e) {
                return;
            }
            post(message);
        }

        // value is already JSON: a pending share's files would otherwise be
        // copied into a JSONObject only to be serialized again.
        void okRaw(String valueJson) {
            post(new StringBuilder(valueJson.length() + 64)
                .append("{\"id\":").append(JSONObject.quote(id))
                .append(",\"ok\":true,\"value\":").append(valueJson).append('}')
                .toString());
        }

        void fail(String code) {
            fail(code, null);
        }

        // message is shown to the user by the page (MoaShareInbox rejects with it).
        void fail(String code, String error) {
            JSONObject message = new JSONObject();
            try {
                message.put("id", id).put("ok", false).put("code", code);
                if (error != null) message.put("error", error);
            } catch (JSONException e) {
                return;
            }
            post(message.toString());
        }

        private void post(JSONObject message) {
            post(message.toString());
        }

        private void post(String message) {
            main.post(() -> {
                try {
                    proxy.postMessage(message);
                } catch (RuntimeException ignored) {
                    // The page that asked is gone.
                }
            });
        }
    }

    private static final String TAG = "MoaBridges";
    static final String AUTH_LOCAL_PORT = "moaNativeAuthLocal";
    static final String AUTH_PAIRED_PORT = "moaNativeAuthPaired";
    static final String PUSH_PORT = "moaNativePushPort";
    static final String SHARE_LOCAL_PORT = "moaShareInboxLocal";
    static final String SHARE_PAIRED_PORT = "moaShareInboxPaired";

    private final WebView webView;
    private final DeviceCredentialStore credentials;
    private final MethodHandler auth;
    private final MethodHandler push;
    private final MethodHandler share;
    private final List<ScriptHandler> pairedScripts = new ArrayList<>();
    private String installedPairedOrigin;
    private boolean supported;

    WebBridges(WebView webView, DeviceCredentialStore credentials, MethodHandler auth, MethodHandler push,
        MethodHandler share) {
        this.webView = webView;
        this.credentials = credentials;
        this.auth = auth;
        this.push = push;
        this.share = share;
    }

    static boolean isSupported() {
        return WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)
            && WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT);
    }

    // Must run before the first page loads, on the UI thread.
    boolean install() {
        supported = isSupported();
        if (!supported) {
            Log.w(TAG, "WebView lacks WebMessageListener or document-start scripts; native bridges disabled");
            return false;
        }
        Set<String> local = Collections.singleton(OriginPolicy.LOCAL_ORIGIN);
        WebViewCompat.addWebMessageListener(webView, AUTH_LOCAL_PORT, local, listener("auth", auth));
        WebViewCompat.addDocumentStartJavaScript(webView, authScript(AUTH_LOCAL_PORT), local);
        WebViewCompat.addWebMessageListener(webView, SHARE_LOCAL_PORT, local, listener("share", share));
        WebViewCompat.addDocumentStartJavaScript(webView, shareScript(SHARE_LOCAL_PORT), local);
        syncPairedOrigin();
        return true;
    }

    // Re-scopes the paired-origin registrations to the origin the credential
    // store holds now. Called before the first page and then only as a
    // main-frame navigation starts (MoaWebViewClient), never under a live
    // page: WebView breaks a page's existing ports when listeners change.
    void syncPairedOrigin() {
        if (!supported) return;
        String paired = OriginPolicy.normalizeOrigin(credentials.pairedOrigin());
        if (paired == null ? installedPairedOrigin == null : paired.equals(installedPairedOrigin)) return;
        if (installedPairedOrigin != null) {
            WebViewCompat.removeWebMessageListener(webView, AUTH_PAIRED_PORT);
            WebViewCompat.removeWebMessageListener(webView, PUSH_PORT);
            WebViewCompat.removeWebMessageListener(webView, SHARE_PAIRED_PORT);
            for (ScriptHandler script : pairedScripts) script.remove();
            pairedScripts.clear();
        }
        installedPairedOrigin = paired;
        if (paired == null) return;
        Set<String> rules = new HashSet<>(Collections.singleton(paired));
        WebViewCompat.addWebMessageListener(webView, AUTH_PAIRED_PORT, rules, listener("auth", auth));
        WebViewCompat.addWebMessageListener(webView, PUSH_PORT, rules, listener("push", push));
        WebViewCompat.addWebMessageListener(webView, SHARE_PAIRED_PORT, rules, listener("share", share));
        pairedScripts.add(WebViewCompat.addDocumentStartJavaScript(webView, authScript(AUTH_PAIRED_PORT), rules));
        pairedScripts.add(WebViewCompat.addDocumentStartJavaScript(webView, pushScript(), rules));
        pairedScripts.add(WebViewCompat.addDocumentStartJavaScript(webView, shareScript(SHARE_PAIRED_PORT), rules));
    }

    private WebViewCompat.WebMessageListener listener(String bridge, MethodHandler handler) {
        return (WebView view, WebMessageCompat message, Uri sourceOrigin, boolean isMainFrame, JavaScriptReplyProxy proxy) -> {
            if (!isMainFrame || message == null || message.getData() == null) return;
            JSONObject body;
            String id;
            String method;
            JSONObject options;
            try {
                body = new JSONObject(message.getData());
                id = body.getString("id");
                method = body.getString("method");
                options = body.optJSONObject("options");
            } catch (JSONException e) {
                return;
            }
            if (options == null) options = new JSONObject();
            Reply reply = new Reply(proxy, id);
            OriginPolicy.Caller caller = OriginPolicy.classify(sourceOrigin.toString(), credentials.pairedOrigin());
            if (!OriginPolicy.mayCall(bridge, method, caller)) {
                reply.fail(caller == OriginPolicy.Caller.OTHER || isKnown(bridge, method) ? "not_authorized" : "unsupported");
                return;
            }
            handler.handle(method, options, caller, reply);
        };
    }

    private static boolean isKnown(String bridge, String method) {
        return OriginPolicy.mayCall(bridge, method, OriginPolicy.Caller.LOCAL)
            || OriginPolicy.mayCall(bridge, method, OriginPolicy.Caller.PAIRED);
    }

    // The same object iOS injects, on a WebMessage port instead of
    // webkit.messageHandlers. Main frame only, like the iOS user scripts.
    static String authScript(String port) {
        return "(() => {\n"
            + "  if (window.top !== window || window.MoaNativeAuth) return;\n"
            + "  const port = window." + port + ";\n"
            + "  if (!port) return;\n"
            + "  let nextId = 1;\n"
            + "  const calls = new Map();\n"
            + "  port.onmessage = (event) => {\n"
            + "    let message;\n"
            + "    try { message = JSON.parse(event.data); } catch (e) { return; }\n"
            + "    const pending = calls.get(message.id);\n"
            + "    if (!pending) return;\n"
            + "    calls.delete(message.id);\n"
            + "    if (message.ok) { pending.resolve(message.value); return; }\n"
            + "    const error = new Error(\"Native authentication failed.\");\n"
            + "    error.code = message.code || \"unavailable\";\n"
            + "    pending.reject(error);\n"
            + "  };\n"
            + "  const call = (method, options = {}) => new Promise((resolve, reject) => {\n"
            + "    const id = String(nextId++);\n"
            + "    calls.set(id, { resolve, reject });\n"
            + "    port.postMessage(JSON.stringify({ id, method, options }));\n"
            + "  });\n"
            + "  window.MoaNativeAuth = Object.freeze({\n"
            + "    claim: (origin, payload, deviceLabel) => call(\"claim\", { origin, payload, deviceLabel }),\n"
            + "    authorize: (origin) => call(\"authorize\", { origin }),\n"
            + "    reauthorize: () => call(\"reauthorize\"),\n"
            + "    reset: () => call(\"reset\"),\n"
            + "  });\n"
            + "})();\n";
    }

    static String pushScript() {
        return "(() => {\n"
            + "  if (window.top !== window || window.MoaNativePush) return;\n"
            + "  const port = window." + PUSH_PORT + ";\n"
            + "  if (!port) return;\n"
            + "  let nextId = 1;\n"
            + "  const calls = new Map();\n"
            + "  port.onmessage = (event) => {\n"
            + "    let message;\n"
            + "    try { message = JSON.parse(event.data); } catch (e) { return; }\n"
            + "    const pending = calls.get(message.id);\n"
            + "    if (!pending) return;\n"
            + "    calls.delete(message.id);\n"
            + "    if (message.ok) { pending.resolve(message.value); return; }\n"
            + "    const error = new Error(\"Native notifications failed.\");\n"
            + "    error.code = message.code || \"unavailable\";\n"
            + "    pending.reject(error);\n"
            + "  };\n"
            + "  const call = (method, options = {}) => new Promise((resolve, reject) => {\n"
            + "    const id = String(nextId++);\n"
            + "    calls.set(id, { resolve, reject });\n"
            + "    port.postMessage(JSON.stringify({ id, method, options }));\n"
            + "  });\n"
            + "  window.MoaNativePush = Object.freeze({\n"
            + "    status: () => call(\"status\"),\n"
            + "    enable: () => call(\"enable\"),\n"
            + "    disable: () => call(\"disable\"),\n"
            + "    setVisibleSession: (session) => call(\"setVisibleSession\", {\n"
            + "      session: typeof session === \"string\" ? session : null\n"
            + "    }),\n"
            + "  });\n"
            + "})();\n";
    }

    // window.MoaShareInbox, the same API as iOS ShareInboxMessageHandler.
    // Errors reject with the native message, which the page shows as is.
    static String shareScript(String port) {
        return "(() => {\n"
            + "  if (window.top !== window || window.MoaShareInbox) return;\n"
            + "  const port = window." + port + ";\n"
            + "  if (!port) return;\n"
            + "  let nextId = 1;\n"
            + "  const calls = new Map();\n"
            + "  port.onmessage = (event) => {\n"
            + "    let message;\n"
            + "    try { message = JSON.parse(event.data); } catch (e) { return; }\n"
            + "    const pending = calls.get(message.id);\n"
            + "    if (!pending) return;\n"
            + "    calls.delete(message.id);\n"
            + "    if (message.ok) { pending.resolve(message.value); return; }\n"
            + "    const error = new Error(message.error || \"Native share failed.\");\n"
            + "    error.code = message.code || \"unavailable\";\n"
            + "    pending.reject(error);\n"
            + "  };\n"
            + "  const call = (method, options = {}) => new Promise((resolve, reject) => {\n"
            + "    const id = String(nextId++);\n"
            + "    calls.set(id, { resolve, reject });\n"
            + "    port.postMessage(JSON.stringify({ id, method, options }));\n"
            + "  });\n"
            + "  window.MoaShareInbox = Object.freeze({\n"
            + "    status: () => call(\"status\"),\n"
            + "    bindServer: (origin) => call(\"bindServer\", { origin }),\n"
            + "    clearServer: () => call(\"clearServer\"),\n"
            + "    peek: () => call(\"peek\"),\n"
            + "    acknowledge: (id) => call(\"acknowledge\", { id }),\n"
            + "  });\n"
            + "})();\n";
    }
}

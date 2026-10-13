package com.ealeixandre.moa.app;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.Locale;

// Which page may call which native method, and where the main frame may go.
// Pure Java so the whole policy is pinned by JVM tests. It mirrors the iOS
// container: the local pairing page (https://localhost here, capacitor://localhost
// on iOS) may claim and authorize; the paired server's own origin may renew its
// session, use notifications and receive navigation; nothing else may do
// anything. Origins are compared as scheme + host + effective port, never as
// prefixes, so another port or a look-alike host is a different origin.
public final class OriginPolicy {

    // Capacitor serves www/ from this origin (androidScheme https, hostname localhost).
    public static final String LOCAL_ORIGIN = "https://localhost";

    public enum Caller {
        LOCAL,
        PAIRED,
        OTHER
    }

    public enum Navigation {
        // Load in the WebView: the paired origin or the local pairing page.
        WEBVIEW,
        // The frontend's iOS-shaped "back to the pairing page" URL
        // (capacitor://localhost...): load the local page instead.
        LOCAL_PAGE,
        // data: and blob: stay with Capacitor's own handling.
        DEFAULT,
        // Everything else leaves the app for the system browser.
        EXTERNAL
    }

    private OriginPolicy() {}

    // Returns "https://host[:port]" for a bare HTTPS origin (no user info, path
    // other than "/", query or fragment), lowercased and without the default
    // port, or null. Same acceptance rules as the iOS validatedOrigin.
    public static String normalizeOrigin(String raw) {
        if (raw == null || raw.isEmpty()) return null;
        URI uri;
        try {
            uri = new URI(raw);
        } catch (URISyntaxException e) {
            return null;
        }
        if (!"https".equalsIgnoreCase(uri.getScheme())) return null;
        String host = uri.getHost();
        if (host == null || host.isEmpty()) return null;
        if (uri.getRawUserInfo() != null) return null;
        String path = uri.getRawPath();
        if (path != null && !path.isEmpty() && !path.equals("/")) return null;
        if (uri.getRawQuery() != null || uri.getRawFragment() != null) return null;
        return origin(host, uri.getPort());
    }

    // The origin of any https URL (path and query allowed), or null.
    public static String originOfUrl(String url) {
        if (url == null) return null;
        URI uri;
        try {
            uri = new URI(url);
        } catch (URISyntaxException e) {
            return null;
        }
        if (!"https".equalsIgnoreCase(uri.getScheme())) return null;
        String host = uri.getHost();
        if (host == null || host.isEmpty() || uri.getRawUserInfo() != null) return null;
        return origin(host, uri.getPort());
    }

    private static String origin(String host, int port) {
        String lower = host.toLowerCase(Locale.ROOT);
        if (port == 443) port = -1;
        if (port == 0 || port > 65535) return null;
        return "https://" + lower + (port > 0 ? ":" + port : "");
    }

    public static boolean isLocal(String origin) {
        return LOCAL_ORIGIN.equals(originOfUrl(origin));
    }

    // True when url is on exactly the paired origin.
    public static boolean matchesPaired(String url, String pairedOrigin) {
        String paired = normalizeOrigin(pairedOrigin);
        if (paired == null) return false;
        return paired.equals(originOfUrl(url));
    }

    public static Caller classify(String sourceOrigin, String pairedOrigin) {
        if (isLocal(sourceOrigin)) return Caller.LOCAL;
        if (matchesPaired(sourceOrigin, pairedOrigin)) return Caller.PAIRED;
        return Caller.OTHER;
    }

    // The table the bridges enforce. Same rows as iOS DeviceAuthBridge.swift,
    // NativePushBridge.swift and the share inbox handler in
    // MoaBridgeViewController.swift; an unknown method is refused by the caller.
    public static boolean mayCall(String bridge, String method, Caller caller) {
        if (caller == Caller.OTHER || bridge == null || method == null) return false;
        if ("auth".equals(bridge)) {
            switch (method) {
                case "claim":
                case "authorize":
                    return caller == Caller.LOCAL;
                case "reauthorize":
                    return caller == Caller.PAIRED;
                case "reset":
                    return true;
                default:
                    return false;
            }
        }
        if ("share".equals(bridge)) {
            switch (method) {
                case "status":
                case "bindServer":
                case "clearServer":
                    return caller == Caller.LOCAL;
                case "peek":
                case "acknowledge":
                    return caller == Caller.PAIRED;
                default:
                    return false;
            }
        }
        if ("push".equals(bridge)) {
            switch (method) {
                case "status":
                case "enable":
                case "disable":
                case "setVisibleSession":
                case "openFallback":
                    return caller == Caller.PAIRED;
                default:
                    return false;
            }
        }
        return false;
    }

    public static Navigation navigation(String url, String pairedOrigin) {
        if (url == null) return Navigation.EXTERNAL;
        String lower = url.toLowerCase(Locale.ROOT);
        if (lower.startsWith("data:") || lower.startsWith("blob:")) return Navigation.DEFAULT;
        if (localPageFor(url) != null) return Navigation.LOCAL_PAGE;
        if (isLocal(url) || matchesPaired(url, pairedOrigin)) return Navigation.WEBVIEW;
        return Navigation.EXTERNAL;
    }

    // capacitor://localhost[/path][?query] -> https://localhost[/path][?query].
    // The remote frontend uses the iOS local origin to return to the pairing
    // page (native-auth.js); Android serves that page from https://localhost.
    public static String localPageFor(String url) {
        if (url == null) return null;
        URI uri;
        try {
            uri = new URI(url);
        } catch (URISyntaxException e) {
            return null;
        }
        if (!"capacitor".equalsIgnoreCase(uri.getScheme())) return null;
        if (!"localhost".equalsIgnoreCase(uri.getHost()) || uri.getPort() != -1 || uri.getRawUserInfo() != null) return null;
        String path = uri.getRawPath() == null || uri.getRawPath().isEmpty() ? "/" : uri.getRawPath();
        String query = uri.getRawQuery() == null ? "" : "?" + uri.getRawQuery();
        return LOCAL_ORIGIN + path + query;
    }

    // The push relay base URL: https, a host, nothing else (iOS relayURL).
    public static boolean validRelayUrl(String raw) {
        if (raw == null || raw.isEmpty() || raw.endsWith("/")) return false;
        URI uri;
        try {
            uri = new URI(raw);
        } catch (URISyntaxException e) {
            return false;
        }
        return "https".equalsIgnoreCase(uri.getScheme())
            && uri.getHost() != null
            && !uri.getHost().isEmpty()
            && uri.getRawUserInfo() == null
            && (uri.getRawPath() == null || uri.getRawPath().isEmpty())
            && uri.getRawQuery() == null
            && uri.getRawFragment() == null;
    }
}

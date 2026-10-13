package com.ealeixandre.moa.app;

import java.util.List;
import java.util.Locale;

// Picks the short-lived browser session out of the /api/pulse/device-session
// response. Same acceptance as iOS installSessionCookie: the exact name,
// host-only, Path=/, Secure, HttpOnly, SameSite Strict or Lax, and not already
// expired. The server issues it Lax (the native first load is a cross-site
// top-level navigation, which Strict would not carry); Strict is what older
// servers send. The raw header is what is handed to CookieManager, so its own
// attributes survive unchanged.
public final class SessionCookie {

    public static final String NAME = "__Host-moa_device";

    private SessionCookie() {}

    public static String select(List<String> setCookieHeaders) {
        if (setCookieHeaders == null) return null;
        for (String header : setCookieHeaders) {
            if (accepts(header)) return header;
        }
        return null;
    }

    // A deliberately small parser: java.net.HttpCookie differs between the
    // JVM and Android and splits on the comma inside Expires.
    static boolean accepts(String header) {
        if (header == null || header.length() > 4096) return false;
        String[] parts = header.split(";");
        int eq = parts[0].indexOf('=');
        if (eq < 0 || !NAME.equals(parts[0].substring(0, eq).trim())) return false;
        if (parts[0].substring(eq + 1).trim().isEmpty()) return false;
        boolean secure = false;
        boolean httpOnly = false;
        boolean sameSite = false;
        boolean rootPath = false;
        long maxAge = 0;
        for (int i = 1; i < parts.length; i++) {
            String attribute = parts[i].trim();
            int at = attribute.indexOf('=');
            String key = (at < 0 ? attribute : attribute.substring(0, at)).trim().toLowerCase(Locale.ROOT);
            String value = at < 0 ? "" : attribute.substring(at + 1).trim();
            switch (key) {
                case "secure":
                    secure = true;
                    break;
                case "httponly":
                    httpOnly = true;
                    break;
                case "samesite":
                    sameSite = "strict".equalsIgnoreCase(value) || "lax".equalsIgnoreCase(value);
                    break;
                case "path":
                    rootPath = "/".equals(value);
                    break;
                case "domain":
                    return false;
                case "max-age":
                    try {
                        maxAge = Long.parseLong(value);
                    } catch (NumberFormatException e) {
                        return false;
                    }
                    break;
                default:
                    break;
            }
        }
        return secure && httpOnly && sameSite && rootPath && maxAge > 0;
    }

    // What clears it: the same name and attributes, already expired.
    public static String expired() {
        return NAME + "=; Path=/; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Secure; HttpOnly; SameSite=Strict";
    }
}

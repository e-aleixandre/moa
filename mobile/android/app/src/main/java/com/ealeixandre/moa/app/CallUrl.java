package com.ealeixandre.moa.app;

import java.net.URI;
import java.net.URISyntaxException;

// The only socket a native call may open: the paired server's own
// /api/voice/calls/{id}/ws over wss. Same rules as iOS callURL; the page
// chooses the URL, so nothing else may get the device credential.
final class CallUrl {

    final String url;
    final String callId;

    private CallUrl(String url, String callId) {
        this.url = url;
        this.callId = callId;
    }

    static CallUrl validate(String raw, String pairedOrigin) {
        if (raw == null || raw.isEmpty() || raw.indexOf('?') >= 0 || raw.indexOf('#') >= 0) return null;
        URI uri;
        try {
            uri = new URI(raw);
        } catch (URISyntaxException e) {
            return null;
        }
        if (!"wss".equalsIgnoreCase(uri.getScheme()) || uri.getRawUserInfo() != null) return null;
        if (uri.getHost() == null || uri.getHost().isEmpty()) return null;
        // Decoded, so an encoded slash splits into more segments and fails.
        String path = uri.getPath();
        if (path == null) return null;
        String[] parts = path.split("/", -1);
        if (parts.length != 6 || !parts[0].isEmpty() || !"api".equals(parts[1]) || !"voice".equals(parts[2])
            || !"calls".equals(parts[3]) || parts[4].isEmpty() || !"ws".equals(parts[5])) {
            return null;
        }
        String https = "https" + raw.substring(raw.indexOf("://"));
        if (!OriginPolicy.matchesPaired(https, pairedOrigin)) return null;
        return new CallUrl(raw, parts[4]);
    }
}

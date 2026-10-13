package com.ealeixandre.moa.app;

import java.io.IOException;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.json.JSONException;

// Talks to the paired moa with the device credential for the share sheet:
// lists destinations and sends. Port of iOS ShareClient.swift. Over NativeHttp
// (HTTPS only, no redirects, no cookie jar); the credential goes only into the
// Authorization header, never into an intent, a log or the WebView.
final class ShareClient {

    static final class ShareException extends Exception {
        enum Kind {
            NOT_PAIRED,
            UNREACHABLE,
            // The request may have reached the server: never retried.
            OUTCOME_UNKNOWN,
            NOT_FOUND,
            SERVER,
            INVALID_RESPONSE
        }

        final Kind kind;
        final int status;

        ShareException(Kind kind) {
            this(kind, 0);
        }

        ShareException(Kind kind, int status) {
            super(kind.name() + (status > 0 ? " " + status : ""));
            this.kind = kind;
            this.status = status;
        }
    }

    // One HTTP exchange. Throws NativeHttp.NotSentException when the request
    // cannot have reached the server, any other IOException when it might.
    interface Transport {
        NativeHttp.Response exchange(String method, String path, Map<String, String> headers, NativeHttp.Body body)
            throws IOException;
    }

    private static final int CONNECT_TIMEOUT_MILLIS = 15_000;
    private static final int LIST_TIMEOUT_MILLIS = 15_000;
    private static final int SEND_TIMEOUT_MILLIS = 60_000;

    static final NativeHttp.Body EMPTY_OBJECT = new NativeHttp.Body() {
        private final byte[] bytes = "{}".getBytes(StandardCharsets.UTF_8);

        @Override
        public long length() {
            return bytes.length;
        }

        @Override
        public void writeTo(OutputStream out) throws IOException {
            out.write(bytes);
        }
    };

    private final String credential;
    private final Transport transport;

    ShareClient(String credential, Transport transport) {
        this.credential = credential;
        this.transport = transport;
    }

    // The client for the stored pairing, or null when there is none usable
    // (unpaired, expired, unreadable or an origin that is not plain HTTPS).
    static ShareClient forStored(DeviceCredentialStore.Stored stored, long nowMillis) {
        if (stored == null || stored.expiresAtMillis <= nowMillis) return null;
        String origin = OriginPolicy.normalizeOrigin(stored.origin);
        if (origin == null || stored.credential == null || stored.credential.isEmpty()) return null;
        return new ShareClient(stored.credential, (method, path, headers, body) -> NativeHttp.send(
            method, origin + path, headers, body, CONNECT_TIMEOUT_MILLIS,
            "GET".equals(method) ? LIST_TIMEOUT_MILLIS : SEND_TIMEOUT_MILLIS));
    }

    List<ShareLogic.Destination> destinations() throws ShareException {
        NativeHttp.Response response;
        try {
            response = transport.exchange("GET", "/api/share/destinations", headers(false), null);
        } catch (IOException e) {
            // A GET changes nothing: any failure is "can't reach moa".
            throw new ShareException(ShareException.Kind.UNREACHABLE);
        }
        check(response);
        try {
            return ShareLogic.parseDestinations(response.body);
        } catch (JSONException e) {
            throw new ShareException(ShareException.Kind.INVALID_RESPONSE);
        }
    }

    // 404 means the conversation is closed: resume it (409 = already active)
    // and send once more. Nothing else is retried.
    void send(String sessionId, NativeHttp.Body body) throws ShareException {
        if (!ShareLogic.isPathSafe(sessionId)) throw new ShareException(ShareException.Kind.NOT_FOUND);
        try {
            post(sessionId, "send", body);
        } catch (ShareException e) {
            if (e.kind != ShareException.Kind.NOT_FOUND) throw e;
            try {
                post(sessionId, "resume", EMPTY_OBJECT);
            } catch (ShareException resume) {
                if (resume.kind == ShareException.Kind.SERVER && resume.status == 409) {
                    // Already active: send below.
                } else if (resume.kind == ShareException.Kind.OUTCOME_UNKNOWN) {
                    // Only the resume is in doubt; the share itself was not
                    // sent, so it can be saved or retried (contract: /resume
                    // failed = certain failure).
                    throw new ShareException(ShareException.Kind.UNREACHABLE);
                } else {
                    throw resume;
                }
            }
            post(sessionId, "send", body);
        }
    }

    private void post(String sessionId, String action, NativeHttp.Body body) throws ShareException {
        NativeHttp.Response response;
        try {
            response = transport.exchange("POST", "/api/sessions/" + sessionId + "/" + action, headers(true), body);
        } catch (NativeHttp.NotSentException e) {
            throw new ShareException(ShareException.Kind.UNREACHABLE);
        } catch (IOException e) {
            throw new ShareException(ShareException.Kind.OUTCOME_UNKNOWN);
        }
        check(response);
    }

    private Map<String, String> headers(boolean json) {
        Map<String, String> headers = new HashMap<>();
        headers.put("X-Moa-Request", "1");
        headers.put("Authorization", "Moa-Device " + credential);
        if (json) headers.put("Content-Type", "application/json");
        return headers;
    }

    private static void check(NativeHttp.Response response) throws ShareException {
        int status = response.status;
        if (status >= 200 && status < 300) return;
        if (status == 401 || status == 403) throw new ShareException(ShareException.Kind.NOT_PAIRED);
        if (status == 404) throw new ShareException(ShareException.Kind.NOT_FOUND);
        throw new ShareException(ShareException.Kind.SERVER, status);
    }
}

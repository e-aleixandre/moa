package com.ealeixandre.moa.app;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.CookieHandler;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import javax.net.ssl.HttpsURLConnection;

// The native HTTP the device credential, the push secret and the relay's send
// key travel over. HTTPS only, redirects refused (a redirect could carry a
// capability to another host), no caches, and no cookie jar: Capacitor's
// CapacitorCookies plugin installs a process-wide CookieHandler that mirrors
// the WebView's cookies, which would attach the browser session to native
// requests and store their Set-Cookie behind our back. iOS gets the same
// isolation from an ephemeral URLSession.
final class NativeHttp {

    static final class Response {
        final int status;
        final String body;
        final Map<String, List<String>> headers;

        Response(int status, String body, Map<String, List<String>> headers) {
            this.status = status;
            this.body = body;
            this.headers = headers;
        }

        List<String> header(String name) {
            for (Map.Entry<String, List<String>> entry : headers.entrySet()) {
                if (entry.getKey() != null && entry.getKey().equalsIgnoreCase(name)) return entry.getValue();
            }
            return Collections.emptyList();
        }
    }

    // A request body written straight to the connection with a known
    // length, so a share's attachments never sit in memory as one string.
    interface Body {
        long length();

        void writeTo(OutputStream out) throws IOException;
    }

    // The request failed before its body was complete: the server cannot have
    // acted on it, so the caller may say "not sent" instead of "unknown".
    static final class NotSentException extends IOException {
        NotSentException(IOException cause) {
            super(cause);
        }
    }

    private static final int MAX_BODY = 64 * 1024;

    private NativeHttp() {}

    static Response send(String method, String url, Map<String, String> headers, String body, int timeoutMillis)
        throws IOException {
        Body bytesBody = null;
        if (body != null) {
            byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
            bytesBody = new Body() {
                @Override
                public long length() {
                    return bytes.length;
                }

                @Override
                public void writeTo(OutputStream out) throws IOException {
                    out.write(bytes);
                }
            };
        }
        return send(method, url, headers, bytesBody, timeoutMillis, timeoutMillis);
    }

    static Response send(String method, String url, Map<String, String> headers, Body body,
        int connectTimeoutMillis, int readTimeoutMillis) throws IOException {
        URL target = new URL(url);
        if (!"https".equals(target.getProtocol())) throw new IOException("https only");
        synchronized (CookieHandler.class) {
            if (CookieHandler.getDefault() != null) CookieHandler.setDefault(null);
        }
        HttpURLConnection connection = (HttpURLConnection) target.openConnection();
        if (!(connection instanceof HttpsURLConnection)) throw new IOException("https only");
        try {
            connection.setInstanceFollowRedirects(false);
            connection.setUseCaches(false);
            connection.setConnectTimeout(connectTimeoutMillis);
            connection.setReadTimeout(readTimeoutMillis);
            connection.setRequestMethod(method);
            for (Map.Entry<String, String> header : headers.entrySet()) {
                connection.setRequestProperty(header.getKey(), header.getValue());
            }
            if (body != null) {
                connection.setDoOutput(true);
                connection.setFixedLengthStreamingMode(body.length());
            }
            try {
                connection.connect();
            } catch (IOException e) {
                throw new NotSentException(e);
            }
            if (body != null) {
                OutputStream out;
                try {
                    out = connection.getOutputStream();
                    body.writeTo(out);
                } catch (IOException e) {
                    throw new NotSentException(e);
                }
                // A failure while flushing the last bytes is not "not sent".
                out.close();
            }
            int status = connection.getResponseCode();
            InputStream stream = status >= 400 ? connection.getErrorStream() : connection.getInputStream();
            return new Response(status, read(stream), connection.getHeaderFields());
        } finally {
            connection.disconnect();
        }
    }

    private static String read(InputStream stream) throws IOException {
        if (stream == null) return "";
        try (InputStream in = stream) {
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            byte[] buffer = new byte[8192];
            int n;
            while ((n = in.read(buffer)) != -1) {
                if (out.size() + n > MAX_BODY) throw new IOException("response too large");
                out.write(buffer, 0, n);
            }
            return out.toString("UTF-8");
        }
    }
}

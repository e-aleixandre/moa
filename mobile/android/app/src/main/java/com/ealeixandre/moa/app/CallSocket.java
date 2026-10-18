package com.ealeixandre.moa.app;

import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import okhttp3.OkHttpClient;
import okhttp3.Request;
import okhttp3.Response;
import okhttp3.WebSocket;
import okhttp3.WebSocketListener;
import okio.ByteString;

// The call's WebSocket, on OkHttp (the platform has none). Like NativeHttp it
// follows no redirect (the device credential must not travel to another
// host) and keeps no cookies; unlike a browser it sends no Origin, which is
// what lets the server tell a native header-authenticated socket apart.
// Callbacks arrive on OkHttp's threads; the host moves them to the call thread.
final class CallSocket implements NativeCallManager.Socket {

    // Half-dead sockets (Wi-Fi to mobile handover) otherwise stay silent
    // until the server gives up: a missed pong fails the socket.
    private static final int PING_SECONDS = 15;
    private static final int CONNECT_SECONDS = 15;
    // A close the server never answers is cut after this long.
    private static final long FORCE_CLOSE_MILLIS = 2000;

    static OkHttpClient newClient() {
        return new OkHttpClient.Builder()
            .followRedirects(false)
            .followSslRedirects(false)
            .retryOnConnectionFailure(false)
            .connectTimeout(CONNECT_SECONDS, TimeUnit.SECONDS)
            .pingInterval(PING_SECONDS, TimeUnit.SECONDS)
            .build();
    }

    private final WebSocket socket;
    private final ScheduledExecutorService timers;

    private CallSocket(WebSocket socket, ScheduledExecutorService timers) {
        this.socket = socket;
        this.timers = timers;
    }

    // url is already validated (CallUrl); ws:// is accepted only because tests
    // run against a local server.
    static CallSocket open(OkHttpClient client, String url, String authorization,
        NativeCallManager.SocketEvents events, ScheduledExecutorService timers) {
        Request request = new Request.Builder().url(url).header("Authorization", authorization).build();
        WebSocket socket = client.newWebSocket(request, new WebSocketListener() {
            @Override
            public void onOpen(WebSocket webSocket, Response response) {
                events.opened();
            }

            @Override
            public void onMessage(WebSocket webSocket, String text) {
                events.text(text);
            }

            @Override
            public void onMessage(WebSocket webSocket, ByteString bytes) {
                events.binary(bytes.toByteArray());
            }

            @Override
            public void onClosing(WebSocket webSocket, int code, String reason) {
                events.closed(code);
                // The call's own close (with why) goes out first; if nobody
                // sent one, answer the server's so the socket does not linger.
                timers.schedule(() -> webSocket.close(1000, null), 1, TimeUnit.SECONDS);
            }

            @Override
            public void onFailure(WebSocket webSocket, Throwable t, Response response) {
                // The exception's class only: its message can carry the URL.
                events.failed(t.getClass().getSimpleName());
            }
        });
        return new CallSocket(socket, timers);
    }

    @Override
    public void sendText(String text) {
        socket.send(text);
    }

    @Override
    public boolean sendBinary(byte[] frame) {
        return socket.send(ByteString.of(frame));
    }

    @Override
    public long queueSize() {
        return socket.queueSize();
    }

    @Override
    public void close(int code, String reason) {
        try {
            socket.close(code, reason);
        } catch (IllegalArgumentException e) {
            socket.cancel();
            return;
        }
        timers.schedule(socket::cancel, FORCE_CLOSE_MILLIS, TimeUnit.MILLISECONDS);
    }
}

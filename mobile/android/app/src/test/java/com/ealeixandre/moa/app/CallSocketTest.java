package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.concurrent.BlockingQueue;
import java.util.concurrent.Executors;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;
import okhttp3.WebSocket;
import okhttp3.WebSocketListener;
import okio.ByteString;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

// The socket adapter against a real local WebSocket server (plain ws: the
// wss-only rule is CallUrl's, tested apart).
public class CallSocketTest {

    private MockWebServer server;
    private ScheduledExecutorService timers;
    private final BlockingQueue<String> client = new LinkedBlockingQueue<>();
    private final BlockingQueue<String> peer = new LinkedBlockingQueue<>();
    private volatile WebSocket serverSide;

    private final NativeCallManager.SocketEvents events = new NativeCallManager.SocketEvents() {
        @Override
        public void opened() {
            client.add("opened");
        }

        @Override
        public void text(String text) {
            client.add("text:" + text);
        }

        @Override
        public void binary(byte[] data) {
            client.add("binary:" + data.length);
        }

        @Override
        public void closed(int code) {
            client.add("closed:" + code);
        }

        @Override
        public void failed(String code) {
            client.add("failed:" + code);
        }
    };

    private final WebSocketListener serverListener = new WebSocketListener() {
        @Override
        public void onOpen(WebSocket webSocket, okhttp3.Response response) {
            serverSide = webSocket;
            peer.add("open");
        }

        @Override
        public void onMessage(WebSocket webSocket, String text) {
            peer.add("text:" + text);
        }

        @Override
        public void onMessage(WebSocket webSocket, ByteString bytes) {
            peer.add("binary:" + bytes.size());
        }

        @Override
        public void onClosing(WebSocket webSocket, int code, String reason) {
            peer.add("closing:" + code + ":" + reason);
            webSocket.close(code, null);
        }
    };

    @Before
    public void start() throws Exception {
        server = new MockWebServer();
        server.start();
        timers = Executors.newSingleThreadScheduledExecutor();
    }

    @After
    public void stop() throws Exception {
        timers.shutdownNow();
        try {
            server.shutdown();
        } catch (Exception ignored) {
            // Already down.
        }
    }

    private CallSocket open() {
        return CallSocket.open(CallSocket.newClient(), server.url("/api/voice/calls/c/ws").toString(),
            "Moa-Device dev_1.secret", events, timers);
    }

    private String next(BlockingQueue<String> queue) throws Exception {
        String item = queue.poll(5, TimeUnit.SECONDS);
        assertNotNull("timed out", item);
        return item;
    }

    @Test
    public void sendsTheCredentialAndNoOrigin() throws Exception {
        server.enqueue(new MockResponse().withWebSocketUpgrade(serverListener));
        open();
        assertEquals("opened", next(client));
        RecordedRequest request = server.takeRequest(5, TimeUnit.SECONDS);
        assertEquals("Moa-Device dev_1.secret", request.getHeader("Authorization"));
        assertNull(request.getHeader("Origin"));
        assertEquals("/api/voice/calls/c/ws", request.getPath());
    }

    @Test
    public void passesTextAndBinaryBothWays() throws Exception {
        server.enqueue(new MockResponse().withWebSocketUpgrade(serverListener));
        CallSocket socket = open();
        assertEquals("opened", next(client));
        assertEquals("open", next(peer));
        serverSide.send("{\"type\":\"pong\"}");
        serverSide.send(ByteString.of(new byte[960]));
        assertEquals("text:{\"type\":\"pong\"}", next(client));
        assertEquals("binary:960", next(client));
        socket.sendText("{\"type\":\"ping\"}");
        assertTrue(socket.sendBinary(new byte[960]));
        assertEquals("text:{\"type\":\"ping\"}", next(peer));
        assertEquals("binary:960", next(peer));
        assertEquals(0, socket.queueSize());
    }

    @Test
    public void reportsTheServersCloseCode() throws Exception {
        server.enqueue(new MockResponse().withWebSocketUpgrade(serverListener));
        open();
        assertEquals("opened", next(client));
        assertEquals("open", next(peer));
        serverSide.close(1001, "bye");
        assertEquals("closed:1001", next(client));
    }

    @Test
    public void ourCloseCarriesTheReason() throws Exception {
        server.enqueue(new MockResponse().withWebSocketUpgrade(serverListener));
        CallSocket socket = open();
        assertEquals("opened", next(client));
        assertEquals("open", next(peer));
        socket.close(1000, "native:page_closed mic=granted");
        assertEquals("closing:1000:native:page_closed mic=granted", next(peer));
    }

    @Test
    public void followsNoRedirect() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(302).setHeader("Location", server.url("/elsewhere").toString()));
        server.enqueue(new MockResponse().withWebSocketUpgrade(serverListener));
        open();
        assertTrue(next(client).startsWith("failed:"));
        assertEquals(1, server.getRequestCount());
    }

    @Test
    public void aRefusedHandshakeIsAFailureNamedByClassOnly() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(401));
        open();
        String failure = next(client);
        assertTrue(failure, failure.startsWith("failed:"));
        assertFalse(failure, failure.contains("localhost"));
        assertFalse(failure, failure.contains("401"));
    }

    @Test
    public void anUnreachableServerFails() throws Exception {
        String url = server.url("/api/voice/calls/c/ws").toString();
        server.shutdown();
        CallSocket.open(CallSocket.newClient(), url, "Moa-Device x.y", events, timers);
        assertEquals("failed:ConnectException", next(client));
    }
}

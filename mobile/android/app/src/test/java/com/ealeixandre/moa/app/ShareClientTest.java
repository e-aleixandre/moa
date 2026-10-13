package com.ealeixandre.moa.app;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.fail;

import com.ealeixandre.moa.app.ShareClient.ShareException;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.Test;

// The send/resume state machine and the destinations request, against a
// scripted transport (the iOS ShareClientTests cases).
public class ShareClientTest {

    private static final class Seen {
        final String method;
        final String path;
        final Map<String, String> headers;
        final byte[] body;

        Seen(String method, String path, Map<String, String> headers, byte[] body) {
            this.method = method;
            this.path = path;
            this.headers = headers;
            this.body = body;
        }
    }

    private final List<Seen> seen = new ArrayList<>();
    private final Deque<Object> script = new ArrayDeque<>();

    private ShareClient client() {
        return new ShareClient("dev.secret", (method, path, headers, body) -> {
            byte[] bytes = null;
            if (body != null) {
                ByteArrayOutputStream out = new ByteArrayOutputStream();
                body.writeTo(out);
                bytes = out.toByteArray();
                assertEquals(body.length(), bytes.length);
            }
            seen.add(new Seen(method, path, new HashMap<>(headers), bytes));
            Object next = script.removeFirst();
            if (next instanceof IOException) throw (IOException) next;
            Object[] response = (Object[]) next;
            return new NativeHttp.Response((Integer) response[0], (String) response[1], Collections.emptyMap());
        });
    }

    private void respond(int status, String body) {
        script.add(new Object[] {status, body});
    }

    private static ShareBody body() {
        return new ShareBody("hi", Collections.emptyList());
    }

    private static ShareException.Kind sendFails(ShareClient client) {
        try {
            client.send("abc", body());
        } catch (ShareException e) {
            return e.kind;
        }
        fail("expected an error");
        return null;
    }

    @Test
    public void sendsOnceWithTheCredentialHeader() throws Exception {
        respond(200, "{}");
        client().send("abc", body());
        assertEquals(1, seen.size());
        assertEquals("POST", seen.get(0).method);
        assertEquals("/api/sessions/abc/send", seen.get(0).path);
        assertEquals("Moa-Device dev.secret", seen.get(0).headers.get("Authorization"));
        assertEquals("1", seen.get(0).headers.get("X-Moa-Request"));
        assertEquals("application/json", seen.get(0).headers.get("Content-Type"));
        assertEquals("{\"text\":\"hi\",\"attachments\":[]}", new String(seen.get(0).body, StandardCharsets.UTF_8));
    }

    @Test
    public void aClosedConversationIsResumedThenSentOnce() throws Exception {
        respond(404, "");
        respond(409, "");
        respond(200, "{}");
        client().send("abc", body());
        assertEquals(3, seen.size());
        assertEquals("/api/sessions/abc/send", seen.get(0).path);
        assertEquals("/api/sessions/abc/resume", seen.get(1).path);
        assertEquals("{}", new String(seen.get(1).body, StandardCharsets.UTF_8));
        assertEquals("/api/sessions/abc/send", seen.get(2).path);
        assertArrayEquals(seen.get(0).body, seen.get(2).body);
    }

    @Test
    public void resumeOkThenSendOk() throws Exception {
        respond(404, "");
        respond(200, "{}");
        respond(200, "{}");
        client().send("abc", body());
        assertEquals(3, seen.size());
    }

    @Test
    public void stillNotFoundAfterResumeStops() {
        respond(404, "");
        respond(200, "{}");
        respond(404, "");
        assertEquals(ShareException.Kind.NOT_FOUND, sendFails(client()));
        assertEquals(3, seen.size());
    }

    @Test
    public void aFailedResumeIsNotFollowedBySend() {
        respond(404, "");
        respond(500, "");
        ShareClient client = client();
        try {
            client.send("abc", body());
            fail();
        } catch (ShareException e) {
            assertEquals(ShareException.Kind.SERVER, e.kind);
            assertEquals(500, e.status);
        }
        assertEquals(2, seen.size());
    }

    @Test
    public void unauthorizedIsNotPairedAndNotRetried() {
        respond(401, "");
        assertEquals(ShareException.Kind.NOT_PAIRED, sendFails(client()));
        assertEquals(1, seen.size());
        respond(403, "");
        assertEquals(ShareException.Kind.NOT_PAIRED, sendFails(client()));
    }

    @Test
    public void aFailureAfterTheBodyLeftIsAmbiguousAndNotRetried() {
        script.add(new IOException("read timed out"));
        assertEquals(ShareException.Kind.OUTCOME_UNKNOWN, sendFails(client()));
        assertEquals(1, seen.size());
    }

    @Test
    public void aFailureBeforeTheBodyLeftIsUnreachable() {
        script.add(new NativeHttp.NotSentException(new IOException("connect refused")));
        assertEquals(ShareException.Kind.UNREACHABLE, sendFails(client()));
        assertEquals(1, seen.size());
    }

    @Test
    public void serverErrorCarriesItsStatus() {
        respond(500, "");
        ShareClient client = client();
        try {
            client.send("abc", body());
            fail();
        } catch (ShareException e) {
            assertEquals(ShareException.Kind.SERVER, e.kind);
            assertEquals(500, e.status);
        }
    }

    @Test
    public void anUnsafeSessionIdNeverReachesTheNetwork() {
        ShareClient client = client();
        try {
            client.send("../owners", body());
            fail();
        } catch (ShareException e) {
            assertEquals(ShareException.Kind.NOT_FOUND, e.kind);
        }
        assertEquals(0, seen.size());
    }

    @Test
    public void destinationsRequestAndDecode() throws Exception {
        respond(200, "{\"destinations\":[{\"kind\":\"owner\",\"session_id\":\"o1\",\"title\":\"moa\"},"
            + "{\"kind\":\"session\",\"session_id\":\"s1\",\"title\":\"Fix\"}]}");
        List<ShareLogic.Destination> list = client().destinations();
        assertEquals(2, list.size());
        assertEquals("o1", list.get(0).sessionId);
        assertEquals("s1", list.get(1).sessionId);
        assertEquals("GET", seen.get(0).method);
        assertEquals("/api/share/destinations", seen.get(0).path);
        assertEquals("Moa-Device dev.secret", seen.get(0).headers.get("Authorization"));
        assertNull(seen.get(0).body);
    }

    @Test
    public void destinationsMapFailures() {
        ShareClient client = client();
        respond(401, "");
        assertEquals(ShareException.Kind.NOT_PAIRED, destinationsFail(client));
        script.add(new IOException("timeout"));
        assertEquals(ShareException.Kind.UNREACHABLE, destinationsFail(client));
        respond(502, "");
        assertEquals(ShareException.Kind.SERVER, destinationsFail(client));
        respond(200, "<html>");
        assertEquals(ShareException.Kind.INVALID_RESPONSE, destinationsFail(client));
    }

    private static ShareException.Kind destinationsFail(ShareClient client) {
        try {
            client.destinations();
        } catch (ShareException e) {
            return e.kind;
        }
        fail("expected an error");
        return null;
    }

    @Test
    public void onlyAnUnexpiredHttpsPairingMakesAClient() {
        long now = 1_000_000;
        assertNotNull(ShareClient.forStored(new DeviceCredentialStore.Stored("https://moa.example:8443", "d.s", now + 1), now));
        assertNull(ShareClient.forStored(null, now));
        assertNull(ShareClient.forStored(new DeviceCredentialStore.Stored("https://moa.example", "d.s", now), now));
        assertNull(ShareClient.forStored(new DeviceCredentialStore.Stored("http://moa.example", "d.s", now + 1), now));
        assertNull(ShareClient.forStored(new DeviceCredentialStore.Stored("https://moa.example/x", "d.s", now + 1), now));
        assertNull(ShareClient.forStored(new DeviceCredentialStore.Stored("https://moa.example", "", now + 1), now));
    }
}

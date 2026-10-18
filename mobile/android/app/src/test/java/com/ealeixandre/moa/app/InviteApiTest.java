package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.concurrent.BlockingQueue;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;
import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

// The invitation's HTTP against a real local server (plain http: https-only is
// the host's rule, since the origin comes from the paired credential).
public class InviteApiTest {

    private static final String AUTH = "Moa-Voice dev_1.key";
    private static final String INV = "0f8fad5b-d9cb-469f-a165-70867728950e";

    private MockWebServer server;
    private InviteApi api;
    private final BlockingQueue<Object[]> replies = new LinkedBlockingQueue<>();
    private final IncomingCalls.Reply reply = (status, body) -> replies.add(new Object[] { status, body });

    @Before
    public void setUp() throws Exception {
        server = new MockWebServer();
        server.start();
        api = new InviteApi(InviteApi.newClient(), Runnable::run);
    }

    @After
    public void tearDown() throws Exception {
        server.shutdown();
    }

    private String origin() {
        return "http://" + server.getHostName() + ":" + server.getPort();
    }

    private Object[] next() throws Exception {
        Object[] r = replies.poll(5, TimeUnit.SECONDS);
        assertNotNull("no reply", r);
        return r;
    }

    @Test
    public void pollWaitsForRingingWithTheVoiceCredentialAndNothingElse() throws Exception {
        server.enqueue(new MockResponse().setBody("{\"state\":\"ringing\"}"));
        api.poll(origin(), AUTH, INV, reply);
        Object[] r = next();
        assertEquals(200, r[0]);
        assertEquals("ringing", ((JSONObject) r[1]).getString("state"));
        RecordedRequest request = server.takeRequest();
        assertEquals("GET", request.getMethod());
        assertEquals("/api/voice/invites/" + INV + "?wait=ringing", request.getPath());
        assertEquals(AUTH, request.getHeader("Authorization"));
        assertNull(request.getHeader("Cookie"));
        assertNull(request.getHeader("Origin"));
    }

    @Test
    public void postSendsTheCsrfHeaderAndAnEmptyJsonBody() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(201).setBody("{\"call_id\":\"c1\",\"ws_url\":\"/api/voice/calls/c1/ws\"}"));
        api.post(origin(), AUTH, "invites/" + INV + "/answer", reply);
        Object[] r = next();
        assertEquals(201, r[0]);
        assertEquals("c1", ((JSONObject) r[1]).getString("call_id"));
        RecordedRequest request = server.takeRequest();
        assertEquals("POST", request.getMethod());
        assertEquals("/api/voice/invites/" + INV + "/answer", request.getPath());
        assertEquals(AUTH, request.getHeader("Authorization"));
        assertEquals("1", request.getHeader("X-Moa-Request"));
        assertTrue(request.getHeader("Content-Type").startsWith("application/json"));
        assertEquals("{}", request.getBody().readUtf8());
    }

    @Test
    public void aBodyThatIsNotJsonIsNoBodyAndEmptyIsFine() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(502).setBody("<html>bad gateway</html>"));
        api.post(origin(), AUTH, "invites/" + INV + "/decline", reply);
        Object[] r = next();
        assertEquals(502, r[0]);
        assertNull(r[1]);
        server.enqueue(new MockResponse().setResponseCode(204));
        api.post(origin(), AUTH, "invites/" + INV + "/decline", reply);
        r = next();
        assertEquals(204, r[0]);
        assertNull(r[1]);
    }

    @Test
    public void aRedirectIsNotFollowed() throws Exception {
        server.enqueue(new MockResponse().setResponseCode(302).setHeader("Location", origin() + "/elsewhere"));
        api.poll(origin(), AUTH, INV, reply);
        assertEquals(302, next()[0]);
        assertEquals(1, server.getRequestCount());
    }

    @Test
    public void anUnreachableServerIsStatusZero() throws Exception {
        String dead = origin();
        server.shutdown();
        api.poll(dead, AUTH, INV, reply);
        assertEquals(0, next()[0]);
        api.post(dead, AUTH, "calls/c1/hangup", reply);
        assertEquals(0, next()[0]);
    }
}

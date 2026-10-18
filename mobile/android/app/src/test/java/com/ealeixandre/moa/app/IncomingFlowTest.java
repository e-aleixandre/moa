package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.Callable;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import okhttp3.mockwebserver.Dispatcher;
import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

// IncomingCalls with the real HTTP against a fake moa server: the whole of
// ring, answer, decline and the late hangup as the server sees them.
public class IncomingFlowTest {

    private static final String AUTH = "Moa-Voice dev_1.key";
    private static final String INV = "0f8fad5b-d9cb-469f-a165-70867728950e";

    private MockWebServer server;
    private ExecutorService thread;
    private InviteApi api;
    private IncomingCalls calls;
    private final List<String> seen = new CopyOnWriteArrayList<>();
    private final List<String> events = new CopyOnWriteArrayList<>();
    private volatile boolean released;
    private volatile int answerStatus = 201;
    private volatile long answerDelayMillis;
    private volatile boolean startOk = true;
    private volatile String startedUrl;

    @Before
    public void setUp() throws Exception {
        server = new MockWebServer();
        server.setDispatcher(new Dispatcher() {
            @Override
            public MockResponse dispatch(RecordedRequest request) {
                seen.add(request.getMethod() + " " + request.getPath() + " " + request.getHeader("Authorization"));
                String path = request.getPath();
                if (path.contains("?wait=ringing")) {
                    // A long poll: held until the test releases it, then "answered".
                    MockResponse r = new MockResponse().setBody("{\"state\":\"" + (released ? "answered" : "ringing") + "\"}");
                    return released ? r : r.setBodyDelay(150, TimeUnit.MILLISECONDS);
                }
                if (path.endsWith("/answer")) {
                    return new MockResponse().setResponseCode(answerStatus)
                        .setBody(answerStatus == 201 ? "{\"call_id\":\"call_1\",\"ws_url\":\"/api/voice/calls/call_1/ws\"}" : "{\"error\":\"answered_elsewhere\"}")
                        .setBodyDelay(answerDelayMillis, TimeUnit.MILLISECONDS);
                }
                return new MockResponse().setResponseCode(204);
            }
        });
        server.start();
        thread = Executors.newSingleThreadExecutor();
        api = new InviteApi(InviteApi.newClient(), thread);
        calls = new IncomingCalls(new IncomingCalls.Host() {
            @Override
            public long now() {
                return System.currentTimeMillis();
            }

            @Override
            public Runnable schedule(Runnable task, long delayMillis) {
                java.util.concurrent.ScheduledFuture<?> f = timer.schedule(() -> thread.execute(task), delayMillis, TimeUnit.MILLISECONDS);
                return () -> f.cancel(false);
            }

            @Override
            public boolean callInProgress() {
                return false;
            }

            @Override
            public void ring(IncomingCalls.Ringing r) {
                events.add("ring");
            }

            @Override
            public void answered(IncomingCalls.Ringing r) {
                events.add("answered");
            }

            @Override
            public void ended(IncomingCalls.Ringing r, IncomingCalls.Ended why) {
                events.add("ended " + why);
            }

            @Override
            public void poll(IncomingCalls.Ringing r, IncomingCalls.Reply reply) {
                api.poll(r.origin, r.authorization, r.invite, reply);
            }

            @Override
            public void post(IncomingCalls.Ringing r, String path, IncomingCalls.Reply reply) {
                api.post(r.origin, r.authorization, path, reply);
            }

            @Override
            public boolean startCall(IncomingCalls.Ringing r, String callId, String url) {
                startedUrl = url;
                events.add("start " + callId);
                return startOk;
            }
        });
    }

    private final java.util.concurrent.ScheduledExecutorService timer = Executors.newSingleThreadScheduledExecutor();

    @After
    public void tearDown() throws Exception {
        timer.shutdownNow();
        thread.shutdownNow();
        server.shutdown();
    }

    private void ring() throws Exception {
        long exp = System.currentTimeMillis() / 1000 + 45;
        CallInvite invite = CallInvite.from(new PushEnvelope.Content("id", PushEnvelope.Destination.session("sess_1"),
            PushEnvelope.Level.URGENT, "th", "Moa", "b", "call", INV, exp));
        call(() -> {
            // The fake server speaks plain http on a random port.
            calls.receive(invite, "http://" + server.getHostName() + ":" + server.getPort(), AUTH);
            return null;
        });
    }

    private <T> T call(Callable<T> task) throws Exception {
        return thread.submit(task).get(5, TimeUnit.SECONDS);
    }

    private void until(String what, Callable<Boolean> condition) throws Exception {
        long deadline = System.currentTimeMillis() + 5000;
        while (System.currentTimeMillis() < deadline) {
            if (condition.call()) return;
            Thread.sleep(20);
        }
        throw new AssertionError("timed out waiting for " + what + "; server saw " + seen + "; events " + events);
    }

    private boolean saw(String line) {
        for (String s : seen) if (s.equals(line)) return true;
        return false;
    }

    @Test
    public void ringingFollowsTheInvitationWithTheVoiceCredential() throws Exception {
        ring();
        until("the poll", () -> saw("GET /api/voice/invites/" + INV + "?wait=ringing " + AUTH));
        assertEquals("ring", events.get(0));
        // Still ringing: it keeps asking.
        until("a second poll", () -> seen.size() >= 2);
        assertEquals(1, events.size());
    }

    @Test
    public void answerJoinsTheCallTheServerOpened() throws Exception {
        ring();
        call(() -> {
            calls.answer(INV);
            return null;
        });
        until("the join", () -> events.contains("start call_1"));
        assertEquals("wss://" + server.getHostName() + ":" + server.getPort() + "/api/voice/calls/call_1/ws", startedUrl.replace("wss://", "wss://"));
        assertTrue(saw("POST /api/voice/invites/" + INV + "/answer " + AUTH));
        assertNull(call(() -> calls.ringingInvite()));
    }

    @Test
    public void answeredElsewhereIsFollowedWithoutAnAnswerRequest() throws Exception {
        ring();
        released = true;
        until("end", () -> events.contains("ended ANSWERED_ELSEWHERE"));
        assertTrue(!saw("POST /api/voice/invites/" + INV + "/answer " + AUTH));
    }

    @Test
    public void aLostRaceIsACleanEnd() throws Exception {
        answerStatus = 409;
        ring();
        call(() -> {
            calls.answer(INV);
            return null;
        });
        until("end", () -> events.contains("ended ANSWER_REFUSED"));
        assertTrue(!events.contains("start call_1"));
    }

    @Test
    public void declineIsSentToTheServer() throws Exception {
        ring();
        call(() -> {
            calls.decline(INV);
            return null;
        });
        until("decline", () -> saw("POST /api/voice/invites/" + INV + "/decline " + AUTH));
        assertTrue(events.contains("ended DECLINED_HERE"));
    }

    @Test
    public void aCallOpenedForAnAnswerThatNobodyWaitsForIsHungUp() throws Exception {
        answerDelayMillis = 300;
        ring();
        call(() -> {
            calls.answer(INV);
            calls.decline(INV);
            return null;
        });
        until("hangup", () -> saw("POST /api/voice/calls/call_1/hangup " + AUTH));
        assertTrue(!events.contains("start call_1"));
        assertTrue(!saw("POST /api/voice/invites/" + INV + "/decline " + AUTH));
    }
}

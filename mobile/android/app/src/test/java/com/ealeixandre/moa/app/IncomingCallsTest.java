package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.List;
import org.json.JSONException;
import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;

// The ringing invitation's life against a fake of every platform edge
// (notification, Telecom, network, the call itself). The contract is iOS
// NativeIncomingCalls', which has no tests of its own.
public class IncomingCallsTest {

    static final String ORIGIN = "https://moa.example.ts.net:8443";
    static final String AUTH = "Moa-Voice dev_1.key";
    static final String INV = "0f8fad5b-d9cb-469f-a165-70867728950e";
    static final String INV2 = "1a8fad5b-d9cb-469f-a165-70867728950e";
    static final long NOW = 1_790_000_000_000L;

    static final class Task {
        final long at;
        final Runnable run;
        boolean cancelled;

        Task(long at, Runnable run) {
            this.at = at;
            this.run = run;
        }
    }

    static final class Request {
        final String path;
        final IncomingCalls.Reply reply;
        boolean answered;

        Request(String path, IncomingCalls.Reply reply) {
            this.path = path;
            this.reply = reply;
        }

        void reply(int status, String json) {
            answered = true;
            try {
                reply.reply(status, json == null ? null : new JSONObject(json));
            } catch (JSONException e) {
                throw new AssertionError(e);
            }
        }
    }

    static final class FakeHost implements IncomingCalls.Host {
        long now = NOW;
        boolean inCall;
        boolean startOk = true;
        final List<String> log = new ArrayList<>();
        final List<Request> polls = new ArrayList<>();
        final List<Request> posts = new ArrayList<>();
        final List<Task> tasks = new ArrayList<>();
        final List<IncomingCalls.Ended> ended = new ArrayList<>();
        String startedCallId;
        String startedUrl;
        IncomingCalls.Ringing started;

        @Override
        public long now() {
            return now;
        }

        @Override
        public Runnable schedule(Runnable task, long delayMillis) {
            Task t = new Task(now + delayMillis, task);
            tasks.add(t);
            return () -> t.cancelled = true;
        }

        @Override
        public boolean callInProgress() {
            return inCall;
        }

        @Override
        public void ring(IncomingCalls.Ringing r) {
            log.add("ring " + r.invite);
        }

        @Override
        public void answered(IncomingCalls.Ringing r) {
            log.add("answered " + r.invite);
        }

        @Override
        public void ended(IncomingCalls.Ringing r, IncomingCalls.Ended why) {
            log.add("ended " + why);
            ended.add(why);
        }

        @Override
        public void poll(IncomingCalls.Ringing r, IncomingCalls.Reply reply) {
            log.add("poll " + r.invite);
            polls.add(new Request("poll", reply));
        }

        @Override
        public void post(IncomingCalls.Ringing r, String path, IncomingCalls.Reply reply) {
            log.add("post " + path);
            posts.add(new Request(path, reply));
        }

        @Override
        public boolean startCall(IncomingCalls.Ringing r, String callId, String url) {
            log.add("start " + callId);
            started = r;
            startedCallId = callId;
            startedUrl = url;
            return startOk;
        }

        void advance(long millis) {
            long target = now + millis;
            while (true) {
                Task next = null;
                for (Task t : tasks) {
                    if (!t.cancelled && t.at <= target && (next == null || t.at < next.at)) next = t;
                }
                if (next == null) break;
                now = next.at;
                next.cancelled = true;
                next.run.run();
            }
            now = target;
        }

        Request lastPoll() {
            return polls.get(polls.size() - 1);
        }

        Request post(String path) {
            for (Request r : posts) if (r.path.equals(path)) return r;
            return null;
        }
    }

    private FakeHost host;
    private IncomingCalls calls;

    @Before
    public void setUp() {
        host = new FakeHost();
        calls = new IncomingCalls(host);
    }

    private static CallInvite invite(String id, long expiresAtSeconds) {
        return CallInvite.from(new PushEnvelope.Content("id", PushEnvelope.Destination.session("sess_1"),
            PushEnvelope.Level.URGENT, "th", "Moa", "b", "call", id, expiresAtSeconds));
    }

    private void receive() {
        calls.receive(invite(INV, NOW / 1000 + 45), ORIGIN, AUTH);
    }

    // ---- receiving

    @Test
    public void ringsBeforeAnyNetworkRequest() {
        receive();
        assertEquals("ring " + INV, host.log.get(0));
        assertEquals("poll " + INV, host.log.get(1));
        assertEquals(INV, calls.ringingInvite());
    }

    @Test
    public void anInvitationThatExpiredDoesNotRingAndAsksNothing() {
        calls.receive(invite(INV, NOW / 1000), ORIGIN, AUTH);
        calls.receive(invite(INV2, NOW / 1000 - 30), ORIGIN, AUTH);
        assertTrue(host.log.isEmpty());
        assertNull(calls.ringingInvite());
    }

    @Test
    public void theSameInvitationTwiceRingsOnce() {
        receive();
        receive();
        assertEquals(1, host.polls.size());
        assertEquals(1, count("ring "));
    }

    @Test
    public void aRepeatedPushAfterTheInvitationEndedDoesNotRingAgain() {
        receive();
        host.lastPoll().reply(200, "{\"state\":\"declined\"}");
        receive();
        assertEquals(1, count("ring "));
        assertNull(calls.ringingInvite());
    }

    @Test
    public void anotherInvitationWhileOneRingsOrACallIsOnDoesNotRing() {
        receive();
        calls.receive(invite(INV2, NOW / 1000 + 45), ORIGIN, AUTH);
        assertEquals(1, count("ring "));
        assertEquals(INV, calls.ringingInvite());

        FakeHost busy = new FakeHost();
        busy.inCall = true;
        IncomingCalls other = new IncomingCalls(busy);
        other.receive(invite(INV, NOW / 1000 + 45), ORIGIN, AUTH);
        assertTrue(busy.log.isEmpty());
    }

    // ---- following the invitation

    @Test
    public void ringingPollsAgain() {
        receive();
        host.lastPoll().reply(200, "{\"state\":\"ringing\"}");
        assertEquals(2, host.polls.size());
        assertTrue(host.ended.isEmpty());
    }

    @Test
    public void anotherDeviceAnsweringStopsTheRinging() {
        for (String state : new String[] { "answering", "answered", "failed" }) {
            setUp();
            receive();
            host.lastPoll().reply(200, "{\"state\":\"" + state + "\"}");
            assertEquals(state, List.of(IncomingCalls.Ended.ANSWERED_ELSEWHERE), host.ended);
            assertNull(calls.ringingInvite());
        }
    }

    @Test
    public void declinedElsewhereAndUnknownAndRefusedEndWithTheirReason() {
        receive();
        host.lastPoll().reply(200, "{\"state\":\"declined\"}");
        assertEquals(List.of(IncomingCalls.Ended.DECLINED_ELSEWHERE), host.ended);

        setUp();
        receive();
        host.lastPoll().reply(404, "{\"error\":\"invite_not_found\"}");
        assertEquals(List.of(IncomingCalls.Ended.REMOTE_ENDED), host.ended);

        setUp();
        receive();
        host.lastPoll().reply(401, null);
        assertEquals(List.of(IncomingCalls.Ended.FAILED), host.ended);
    }

    @Test
    public void aNetworkErrorRetriesAfterTwoSeconds() {
        receive();
        host.lastPoll().reply(0, null);
        assertEquals(1, host.polls.size());
        host.advance(1999);
        assertEquals(1, host.polls.size());
        host.advance(1);
        assertEquals(2, host.polls.size());
        assertTrue(host.ended.isEmpty());
    }

    @Test
    public void nobodyAnsweringEndsItAtExpiryPlusTwoSecondsAsMissed() {
        receive();
        host.advance(46_999);
        assertTrue(host.ended.isEmpty());
        host.advance(1);
        assertEquals(List.of(IncomingCalls.Ended.UNANSWERED), host.ended);
        // The poll that was still out changes nothing when it comes back.
        host.lastPoll().reply(200, "{\"state\":\"ringing\"}");
        host.lastPoll().reply(0, null);
        host.advance(10_000);
        assertEquals(1, host.polls.size());
        assertEquals(1, host.ended.size());
    }

    @Test
    public void aPollThatComesBackAfterTheEndIsIgnored() {
        receive();
        calls.decline(INV);
        host.polls.get(0).reply(200, "{\"state\":\"answered\"}");
        assertEquals(List.of(IncomingCalls.Ended.DECLINED_HERE), host.ended);
    }

    // ---- answering

    @Test
    public void answerPostsToTheInvitationAndJoinsTheCall() {
        receive();
        calls.answer(INV);
        assertEquals(1, host.posts.size());
        assertEquals("invites/" + INV + "/answer", host.posts.get(0).path);
        host.posts.get(0).reply(201, "{\"call_id\":\"call_1\",\"ws_url\":\"/api/voice/calls/call_1/ws\"}");
        assertEquals("call_1", host.startedCallId);
        assertEquals("wss://moa.example.ts.net:8443/api/voice/calls/call_1/ws", host.startedUrl);
        assertTrue(host.log.contains("answered " + INV));
        assertNull(calls.ringingInvite());
        assertTrue(host.ended.isEmpty());
        // Nothing times out later: the call is the manager's now.
        host.advance(120_000);
        assertTrue(host.ended.isEmpty());
        assertEquals(1, host.posts.size());
    }

    @Test
    public void answeringTwiceOrTheWrongInvitationDoesNothingMore() {
        receive();
        calls.answer(INV2);
        assertTrue(host.posts.isEmpty());
        calls.answer(INV);
        calls.answer(INV);
        assertEquals(1, host.posts.size());
    }

    @Test
    public void aPollAnswerWhileAnsweringDoesNotEndTheRinging() {
        receive();
        calls.answer(INV);
        host.polls.get(0).reply(200, "{\"state\":\"answering\"}");
        assertTrue(host.ended.isEmpty());
        assertEquals(1, host.polls.size());
        host.advance(60_000);
        assertTrue(host.ended.isEmpty());
    }

    @Test
    public void answerRefusedOrUnreachableEndsCleanly() {
        for (int status : new int[] { 409, 404, 0, 500, 401 }) {
            setUp();
            receive();
            calls.answer(INV);
            host.posts.get(0).reply(status, status == 409 ? "{\"error\":\"answered_elsewhere\"}" : null);
            assertEquals("status " + status, List.of(IncomingCalls.Ended.ANSWER_REFUSED), host.ended);
            assertNull(host.startedCallId);
            assertEquals(1, host.posts.size());
            assertNull(calls.ringingInvite());
        }
    }

    @Test
    public void aCallThatCannotBeJoinedIsHungUp() {
        host.startOk = false;
        receive();
        calls.answer(INV);
        host.posts.get(0).reply(201, "{\"call_id\":\"call_1\",\"ws_url\":\"/api/voice/calls/call_1/ws\"}");
        assertEquals("calls/call_1/hangup", host.posts.get(1).path);
        assertEquals(List.of(IncomingCalls.Ended.ANSWER_REFUSED), host.ended);
    }

    @Test
    public void anAnswerWithoutAUsableCallIsRefused() {
        receive();
        calls.answer(INV);
        host.posts.get(0).reply(201, "{\"call_id\":\"bad id!\",\"ws_url\":\"/api/voice/calls/x/ws\"}");
        assertNull(host.startedCallId);
        assertEquals(List.of(IncomingCalls.Ended.ANSWER_REFUSED), host.ended);
        assertEquals(1, host.posts.size());

        setUp();
        receive();
        calls.answer(INV);
        host.posts.get(0).reply(201, "{\"call_id\":\"call_1\"}");
        assertNull(host.startedCallId);
        assertEquals("calls/call_1/hangup", host.posts.get(1).path);
    }

    // ---- declining

    @Test
    public void declinePostsAndStopsTheRinging() {
        receive();
        calls.decline(INV);
        assertEquals("invites/" + INV + "/decline", host.posts.get(0).path);
        assertEquals(List.of(IncomingCalls.Ended.DECLINED_HERE), host.ended);
        assertNull(calls.ringingInvite());
        host.advance(120_000);
        assertEquals(1, host.ended.size());
    }

    @Test
    public void declineOfAnotherInvitationOrNoneIsIgnored() {
        calls.decline(INV);
        receive();
        calls.decline(INV2);
        assertTrue(host.posts.isEmpty());
        assertTrue(host.ended.isEmpty());
    }

    @Test
    public void endedWhileTheAnswerWasInFlightHangsUpWhateverCallItGot() {
        receive();
        calls.answer(INV);
        calls.decline(INV);
        // While the answer is out, decline only drops the ringing.
        assertEquals(1, host.posts.size());
        assertEquals(List.of(IncomingCalls.Ended.DECLINED_HERE), host.ended);
        host.posts.get(0).reply(201, "{\"call_id\":\"call_9\",\"ws_url\":\"/api/voice/calls/call_9/ws\"}");
        assertNull(host.startedCallId);
        assertEquals("calls/call_9/hangup", host.posts.get(1).path);
        assertEquals(1, host.ended.size());
    }

    // ---- forgetting

    @Test
    public void forgettingEndsTheRingingAndAnAnswerInFlightIsHungUp() {
        receive();
        calls.answer(INV);
        calls.forgetLocal();
        assertEquals(List.of(IncomingCalls.Ended.FAILED), host.ended);
        host.posts.get(0).reply(201, "{\"call_id\":\"call_9\",\"ws_url\":\"/api/voice/calls/call_9/ws\"}");
        assertNull(host.startedCallId);
        assertEquals("calls/call_9/hangup", host.posts.get(1).path);
    }

    @Test
    public void forgettingWhileRingingStopsItAndLaterPollsDoNothing() {
        receive();
        calls.forgetLocal();
        assertEquals(List.of(IncomingCalls.Ended.FAILED), host.ended);
        host.lastPoll().reply(200, "{\"state\":\"ringing\"}");
        assertEquals(1, host.polls.size());
        // A fresh invitation rings once this device has a secret again.
        calls.receive(invite(INV2, NOW / 1000 + 45), ORIGIN, AUTH);
        assertEquals(1, count("ring " + INV2));
    }

    private int count(String prefix) {
        int n = 0;
        for (String line : host.log) if (line.startsWith(prefix)) n++;
        return n;
    }
}

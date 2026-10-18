package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNull;

import org.json.JSONObject;
import org.junit.Test;

// What one long-poll answer on the invitation means while the phone rings.
// The table is iOS NativeIncomingCalls.follow's.
public class InvitePollTest {

    private static InvitePoll.Outcome of(int status, String state) {
        return InvitePoll.outcome(status, state);
    }

    @Test
    public void ringingKeepsPolling() {
        assertEquals(InvitePoll.Outcome.RINGING, of(200, "ringing"));
    }

    @Test
    public void answeredAnsweringOrFailedMeansAnotherDeviceTookIt() {
        assertEquals(InvitePoll.Outcome.ANSWERED_ELSEWHERE, of(200, "answering"));
        assertEquals(InvitePoll.Outcome.ANSWERED_ELSEWHERE, of(200, "answered"));
        assertEquals(InvitePoll.Outcome.ANSWERED_ELSEWHERE, of(200, "failed"));
    }

    @Test
    public void declinedElsewhere() {
        assertEquals(InvitePoll.Outcome.DECLINED_ELSEWHERE, of(200, "declined"));
    }

    @Test
    public void anyOtherStateOrAnUnknownInvitationEndsTheRinging() {
        assertEquals(InvitePoll.Outcome.REMOTE_ENDED, of(200, "cancelled"));
        assertEquals(InvitePoll.Outcome.REMOTE_ENDED, of(200, "expired"));
        assertEquals(InvitePoll.Outcome.REMOTE_ENDED, of(200, "something-new"));
        assertEquals(InvitePoll.Outcome.REMOTE_ENDED, of(200, null));
        assertEquals(InvitePoll.Outcome.REMOTE_ENDED, of(404, null));
        assertEquals(InvitePoll.Outcome.REMOTE_ENDED, of(404, "ringing"));
    }

    @Test
    public void aRefusedCredentialIsAFailure() {
        assertEquals(InvitePoll.Outcome.FAILED, of(401, null));
        assertEquals(InvitePoll.Outcome.FAILED, of(403, "ringing"));
    }

    @Test
    public void networkErrorsAndServerTroubleRetryLater() {
        assertEquals(InvitePoll.Outcome.RETRY, of(0, null));
        assertEquals(InvitePoll.Outcome.RETRY, of(500, null));
        assertEquals(InvitePoll.Outcome.RETRY, of(502, "ringing"));
        assertEquals(InvitePoll.Outcome.RETRY, of(429, null));
    }

    @Test
    public void readsTheStateFromTheBody() throws Exception {
        assertEquals("ringing", InvitePoll.stateOf(new JSONObject("{\"id\":\"x\",\"state\":\"ringing\"}")));
        assertNull(InvitePoll.stateOf(new JSONObject("{\"state\":3}")));
        assertNull(InvitePoll.stateOf(new JSONObject("{}")));
        assertNull(InvitePoll.stateOf(null));
    }
}

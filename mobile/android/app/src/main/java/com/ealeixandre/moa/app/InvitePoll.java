package com.ealeixandre.moa.app;

import org.json.JSONObject;

// What one long-poll answer on the invitation says while the phone rings
// (iOS NativeIncomingCalls.follow). Cancelling never travels as a push: the
// phone follows the invitation on its server.
final class InvitePoll {

    enum Outcome {
        // Still ringing: ask again.
        RINGING,
        ANSWERED_ELSEWHERE,
        DECLINED_ELSEWHERE,
        // Cancelled, expired or unknown to the server.
        REMOTE_ENDED,
        // The server refuses this credential.
        FAILED,
        // Unreachable or unwell: ask again in a moment.
        RETRY
    }

    private InvitePoll() {}

    static Outcome outcome(int status, String state) {
        if (status == 200) {
            if (state == null) return Outcome.REMOTE_ENDED;
            switch (state) {
                case "ringing":
                    return Outcome.RINGING;
                case "answering":
                case "answered":
                case "failed":
                    return Outcome.ANSWERED_ELSEWHERE;
                case "declined":
                    return Outcome.DECLINED_ELSEWHERE;
                default:
                    return Outcome.REMOTE_ENDED;
            }
        }
        if (status == 404) return Outcome.REMOTE_ENDED;
        if (status == 401 || status == 403) return Outcome.FAILED;
        return Outcome.RETRY;
    }

    static String stateOf(JSONObject body) {
        if (body == null) return null;
        Object state = body.opt("state");
        return state instanceof String ? (String) state : null;
    }
}

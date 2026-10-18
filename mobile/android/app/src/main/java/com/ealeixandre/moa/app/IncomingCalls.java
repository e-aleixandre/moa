package com.ealeixandre.moa.app;

import java.util.ArrayDeque;
import java.util.Deque;
import org.json.JSONObject;

// The one invitation that may be ringing: the Android port of iOS
// NativeIncomingCalls' ringing half. A push rings the phone first and asks the
// network after; the invitation is then followed with a long poll (the only
// way to stop it ringing, never a second push) until it is answered,
// declined, taken elsewhere or runs out. Pure Java over Host, which is every
// platform edge, so JVM tests pin the whole of it.
//
// Threading: every method here and every callback a Host delivers runs on
// the host's call thread; ringingInvite alone may be read from anywhere.
final class IncomingCalls {

    enum Ended {
        // Nobody picked up in time: a missed call.
        UNANSWERED,
        ANSWERED_ELSEWHERE,
        DECLINED_ELSEWHERE,
        REMOTE_ENDED,
        // The server refused the credential, or the secret was forgotten.
        FAILED,
        DECLINED_HERE,
        // The answer lost the race, or the call could not be joined.
        ANSWER_REFUSED
    }

    interface Reply {
        // status 0: the server could not be reached. body: a JSON object or null.
        void reply(int status, JSONObject body);
    }

    static final class Ringing {
        final String invite;
        final String name;
        final String sessionId;
        final String origin;
        final String authorization;
        final long expiresAtMillis;
        boolean answering;
        Runnable expiry;

        Ringing(String invite, String name, String sessionId, String origin, String authorization, long expiresAtMillis) {
            this.invite = invite;
            this.name = name;
            this.sessionId = sessionId;
            this.origin = origin;
            this.authorization = authorization;
            this.expiresAtMillis = expiresAtMillis;
        }
    }

    interface Host {
        long now();

        // Runs task on the call thread later; the result cancels it.
        Runnable schedule(Runnable task, long delayMillis);

        // A call is going on: another invitation is left to expire.
        boolean callInProgress();

        // Show it ringing (notification, call screen, Telecom). Never fails
        // the invitation: whatever the system refuses, the rest goes on.
        void ring(Ringing r);

        // The answer was taken: stop ringing and hand the system call over.
        void answered(Ringing r);

        // Stop ringing for good. UNANSWERED is the missed call.
        void ended(Ringing r, Ended why);

        // GET invites/{id}?wait=ringing
        void poll(Ringing r, Reply reply);

        // POST /api/voice/<path> with an empty JSON body.
        void post(Ringing r, String path, Reply reply);

        // Joins the call the server opened for the answer.
        boolean startCall(Ringing r, String callId, String url);
    }

    private static final long RETRY_MILLIS = 2000;
    // The server's expiry is the authority; this is only slack for clocks.
    private static final long EXPIRY_SLACK_MILLIS = 2000;
    private static final int REMEMBERED = 16;

    private final Host host;
    private volatile Ringing ringing;
    // Invitations already shown or refused: a push repeated after the
    // invitation ended must not ring again.
    private final Deque<String> seen = new ArrayDeque<>();

    IncomingCalls(Host host) {
        this.host = host;
    }

    // The invitation ringing now, readable from any thread.
    String ringingInvite() {
        Ringing r = ringing;
        return r == null ? null : r.invite;
    }

    void receive(CallInvite invite, String origin, String authorization) {
        if (invite == null || host.now() >= invite.expiresAtSeconds * 1000) return;
        String key = invite.id.toLowerCase(java.util.Locale.ROOT);
        if (seen.contains(key)) return;
        // One at a time; another one is left to run out, as iOS does.
        if (ringing != null || host.callInProgress()) return;
        seen.addLast(key);
        if (seen.size() > REMEMBERED) seen.removeFirst();
        Ringing r = new Ringing(invite.id, invite.name.isEmpty() ? "moa" : invite.name, invite.sessionId, origin,
            authorization, invite.expiresAtSeconds * 1000);
        ringing = r;
        // The phone rings before anything asks the network.
        host.ring(r);
        if (ringing != r) return;
        r.expiry = host.schedule(() -> {
            if (ringing == r && !r.answering) end(r, Ended.UNANSWERED);
        }, Math.max(0, r.expiresAtMillis + EXPIRY_SLACK_MILLIS - host.now()));
        follow(r);
    }

    private boolean current(Ringing r) {
        return ringing == r && !r.answering;
    }

    private void follow(Ringing r) {
        if (!current(r)) return;
        host.poll(r, (status, body) -> {
            if (!current(r)) return;
            switch (InvitePoll.outcome(status, InvitePoll.stateOf(body))) {
                case RINGING:
                    follow(r);
                    break;
                case ANSWERED_ELSEWHERE:
                    end(r, Ended.ANSWERED_ELSEWHERE);
                    break;
                case DECLINED_ELSEWHERE:
                    end(r, Ended.DECLINED_ELSEWHERE);
                    break;
                case REMOTE_ENDED:
                    end(r, Ended.REMOTE_ENDED);
                    break;
                case FAILED:
                    end(r, Ended.FAILED);
                    break;
                default:
                    host.schedule(() -> follow(r), RETRY_MILLIS);
            }
        });
    }

    private void end(Ringing r, Ended why) {
        if (ringing != r) return;
        ringing = null;
        if (r.expiry != null) r.expiry.run();
        r.expiry = null;
        host.ended(r, why);
    }

    // The user answered (notification, call screen, headset or car).
    void answer(String invite) {
        Ringing r = ringing;
        if (r == null || r.answering || !r.invite.equalsIgnoreCase(invite)) return;
        r.answering = true;
        host.post(r, "invites/" + r.invite + "/answer", (status, body) -> {
            String callId = body == null ? null : text(body, "call_id");
            if (ringing != r) {
                // Ended or forgotten while the answer was out: the call the
                // server opened for it is hung up, not joined.
                if (status == 201) hangUp(r, callId);
                return;
            }
            ringing = null;
            if (r.expiry != null) r.expiry.run();
            r.expiry = null;
            String wsPath = body == null ? null : text(body, "ws_url");
            if (status != 201 || !PushEnvelope.isSessionId(callId) || wsPath == null) {
                if (status == 201) hangUp(r, callId);
                host.ended(r, Ended.ANSWER_REFUSED);
                return;
            }
            host.answered(r);
            String url = r.origin.replaceFirst("^https?://", "wss://") + wsPath;
            if (!host.startCall(r, callId, url)) {
                hangUp(r, callId);
                host.ended(r, Ended.ANSWER_REFUSED);
            }
        });
    }

    // The user declined. While the answer is out, decline only drops the
    // ringing: the answer's reply hangs up whatever call it got.
    void decline(String invite) {
        Ringing r = ringing;
        if (r == null || !r.invite.equalsIgnoreCase(invite)) return;
        boolean answering = r.answering;
        end(r, Ended.DECLINED_HERE);
        if (!answering) host.post(r, "invites/" + r.invite + "/decline", (status, body) -> { });
    }

    // Unpaired, paired again or revoked: the secret goes, and a ringing
    // invitation with it.
    void forgetLocal() {
        Ringing r = ringing;
        if (r != null) end(r, Ended.FAILED);
    }

    private void hangUp(Ringing r, String callId) {
        if (PushEnvelope.isSessionId(callId)) host.post(r, "calls/" + callId + "/hangup", (status, body) -> { });
    }

    private static String text(JSONObject body, String name) {
        Object value = body.opt(name);
        return value instanceof String ? (String) value : null;
    }
}

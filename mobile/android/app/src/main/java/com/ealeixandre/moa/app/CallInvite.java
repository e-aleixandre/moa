package com.ealeixandre.moa.app;

import java.util.regex.Pattern;

// What an opened k:"call" envelope asks the phone to ring for (iOS
// NativeIncomingCalls.receive): a call to a session, with the invitation id
// the server follows for answer, decline and cancel.
final class CallInvite {

    private static final Pattern UUID = Pattern.compile(
        "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}");

    final String id;
    final String name;
    final String sessionId;
    final long expiresAtSeconds;

    private CallInvite(String id, String name, String sessionId, long expiresAtSeconds) {
        this.id = id;
        this.name = name;
        this.sessionId = sessionId;
        this.expiresAtSeconds = expiresAtSeconds;
    }

    // The invitation in an FCM data message, or null when it is not for this
    // call secret, does not open, has expired or is not a call. Never anything
    // is shown for a message that fails here.
    static CallInvite fromPush(java.util.Map<String, String> data, PushEnvelope.Keys keys, long nowSeconds) {
        if (!PushMessages.addressedTo(data, keys)) return null;
        return from(PushEnvelope.open(data, keys, nowSeconds));
    }

    // null when the envelope is not a call this app can ring for.
    static CallInvite from(PushEnvelope.Content content) {
        if (content == null || !"call".equals(content.kind)) return null;
        if (content.invite == null || !UUID.matcher(content.invite).matches()) return null;
        if (!"session".equals(content.destination.kind)) return null;
        return new CallInvite(content.invite, CallProtocol.prefix(content.title, 80), content.destination.session,
            content.expiresAt);
    }
}

package com.ealeixandre.moa.app;

import static org.junit.Assert.*;
import java.util.*;
import org.junit.Test;

// Adversarial schedules against production state machines, using the branch's own test adapters.
public class ReviewAndroidAdversarialTest {
    private static final String ORIGIN = NativeCallManagerTest.ORIGIN;
    private static final String INV = IncomingCallsTest.INV;

    private static CallInvite invite(long exp) {
        return CallInvite.from(new PushEnvelope.Content("id", PushEnvelope.Destination.session("sess_1"),
            PushEnvelope.Level.URGENT, "th", "Moa", "b", "call", INV, exp));
    }

    @Test public void adoptionWhileWaitingForHangupMustNotReopenMicrophone() {
        NativeCallManagerTest.FakeHost host = new NativeCallManagerTest.FakeHost();
        NativeCallManager manager = new NativeCallManager(host);
        manager.open("n1", NativeCallManagerTest.URL, "Moa", "sess_1");
        host.socket().events.opened();
        host.system.listener.ready();
        manager.startAudio("n1");
        host.system.listener.hangUp();
        assertTrue(host.audio().log.contains("stop"));
        // Real reload path: active() -> adoption -> startAudio(snap.id), before ended or the 5 s timer.
        manager.reportActive();
        manager.startAudio("n1");
        assertEquals("Hangup must be terminal for microphone capture", 1, host.audios.size());
    }

    @Test public void changingTheDeviceOnTheSameOriginMustEndTheOldAuthenticatedCall() {
        NativeCallManagerTest.FakeHost host = new NativeCallManagerTest.FakeHost();
        NativeCallManager manager = new NativeCallManager(host);
        manager.open("n1", NativeCallManagerTest.URL, "Moa", "sess_1");
        host.socket().events.opened();
        // DeviceAuthBridge.claim saves the new credential before bindingChanged.
        host.credential = "dev_2.new-secret";
        manager.bindingChanged();
        assertFalse("A new pairing must not adopt the old device's call", manager.hasCall());
    }

    @Test public void serverExpiryBeforeTheSlackTimerMustStillProduceAMissedCall() {
        IncomingCallsTest.FakeHost host = new IncomingCallsTest.FakeHost();
        IncomingCalls calls = new IncomingCalls(host);
        calls.receive(invite(host.now / 1000 + 45), ORIGIN, IncomingCallsTest.AUTH);
        host.advance(45_000);
        host.lastPoll().reply(200, "{\"state\":\"expired\"}");
        host.advance(2_001);
        assertEquals(List.of(IncomingCalls.Ended.UNANSWERED), host.ended);
    }

    @Test public void aPushAdmittedBeforeForgetMustNotRingWhenItsQueuedDeliveryRunsAfterForget() {
        IncomingCallsTest.FakeHost host = new IncomingCallsTest.FakeHost();
        IncomingCalls calls = new IncomingCalls(host);
        CallInvite admitted = invite(host.now / 1000 + 45);
        // AndroidVoip.receive reads the epoch before validating the old secret/credential, but has not yet posted NativeCalls.ring.
        int epoch = calls.epoch();
        calls.forgetLocal();
        // The old FCM thread resumes and posts the captured invitation/auth after forgetRinging's task.
        calls.receive(admitted, ORIGIN, IncomingCallsTest.AUTH, epoch);
        assertNull("An old validated push must be fenced by forgetLocal", calls.ringingInvite());
        assertTrue(host.posts.isEmpty());
    }

    @Test public void forgetAfterReadingTheSecretMustPreventArmingAStaleRelayRegistration() {
        VoipRegistrationTest.MemoryBox box = new VoipRegistrationTest.MemoryBox();
        CallSecretStore secrets = new CallSecretStore(box);
        secrets.save(new CallSecretStore.Secret(ORIGIN, "dev_1", new byte[32]));
        List<String> sent = new ArrayList<>();
        VoipRegistration[] registration = new VoipRegistration[1];
        VoipRegistration.Memory memory = new VoipRegistration.Memory() {
            boolean interleaved;
            public String get(String key) {
                // Stored S_v has been read, but no relay request has begun. An unpair can run here.
                if (!interleaved) { interleaved = true; registration[0].forgetLocal(); }
                return null;
            }
            public void put(String k, String v) {}
            public void clear() {}
        };
        VoipRegistration.Host host = new VoipRegistration.Host() {
            public boolean foreground() { return true; }
            public DeviceCredentialStore.Stored paired() {
                return new DeviceCredentialStore.Stored(ORIGIN, "dev_1.secret", IncomingCallsTest.NOW + 86_400_000);
            }
            public String relayUrl() { return "https://push.example"; }
            public String fcmToken() { return "fcm-test-token"; }
            public long now() { return IncomingCallsTest.NOW; }
            public NativeHttp.Response send(String method, String url, Map<String,String> headers, String body) {
                sent.add(method + " " + url);
                String json = url.endsWith("/status")
                    ? "{\"registered\":true,\"relay_url\":\"https://push.example\",\"expires_at\":" + (now()/1000 + 60L*86400) + "}"
                    : "{}";
                return new NativeHttp.Response(url.endsWith("/status") ? 200 : 202, json, Collections.emptyMap());
            }
        };
        registration[0] = new VoipRegistration(host, secrets, memory, 25);
        registration[0].refresh(false);
        assertEquals("Only the already-started status request is permitted after forget", 1, sent.size());
    }
}

package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.io.IOException;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.json.JSONException;
import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;

// The incoming-call destination's registration (iOS refreshRegistration and
// handleChallenge): what is asked, in what order, and what stops it.
public class VoipRegistrationTest {

    static final String ORIGIN = "https://moa.example.ts.net:8443";
    static final String RELAY = "https://push.letmoa.run";
    static final String CHALLENGE = "Y2hhbGxlbmdlLWNoYWxsZW5nZS1jaGFsbGVuZ2U";
    static final long NOW = 1_790_000_000_000L;
    static final String TOKEN = "f".repeat(152);
    static final String HANDLE = "h".repeat(120);

    static final class Sent {
        final String method;
        final String url;
        final Map<String, String> headers;
        final String body;

        Sent(String method, String url, Map<String, String> headers, String body) {
            this.method = method;
            this.url = url;
            this.headers = headers;
            this.body = body;
        }

        JSONObject json() {
            try {
                return new JSONObject(body);
            } catch (JSONException e) {
                throw new AssertionError(e);
            }
        }
    }

    static final class MemoryBox implements SecretBox {
        byte[] value;

        @Override
        public byte[] load() {
            return value == null ? null : value.clone();
        }

        @Override
        public void save(byte[] plaintext) {
            value = plaintext.clone();
        }

        @Override
        public void delete() {
            value = null;
        }
    }

    static final class Memory implements VoipRegistration.Memory {
        final Map<String, String> values = new HashMap<>();

        @Override
        public String get(String key) {
            return values.get(key);
        }

        @Override
        public void put(String key, String value) {
            values.put(key, value);
        }

        @Override
        public void clear() {
            values.clear();
        }
    }

    final class FakeHost implements VoipRegistration.Host {
        boolean foreground = true;
        DeviceCredentialStore.Stored paired = new DeviceCredentialStore.Stored(ORIGIN, "dev_1.credential", NOW + 86_400_000L);
        String relayUrl = RELAY;
        long now = NOW;
        String token = TOKEN;
        IOException tokenFailure;
        final List<Sent> sent = new java.util.concurrent.CopyOnWriteArrayList<>();
        // Status of GET /api/push/voip/status and what it says.
        int statusCode = 200;
        String statusBody = "{\"registered\":false,\"relay_url\":\"" + RELAY + "\"}";
        int registerCode = 202;
        int postCode = 204;
        // What the relay's /v1/confirm answers when the proof is right.
        String confirmType = "voip";
        String confirmToken = TOKEN;
        String confirmEnv = "production";
        boolean deliverChallenge = true;
        Runnable beforeChallenge;
        Runnable duringServerPost;
        // What the registration asked to be run again, and when.
        final List<Long> retryDelays = new ArrayList<>();
        final List<Runnable> retryTasks = new ArrayList<>();

        @Override
        public void retryLater(long delayMillis, Runnable task) {
            retryDelays.add(delayMillis);
            retryTasks.add(task);
        }

        @Override
        public boolean foreground() {
            return foreground;
        }

        @Override
        public DeviceCredentialStore.Stored paired() {
            return paired;
        }

        @Override
        public String relayUrl() {
            return relayUrl;
        }

        @Override
        public String fcmToken() throws IOException {
            if (tokenFailure != null) throw tokenFailure;
            return token;
        }

        @Override
        public long now() {
            return now;
        }

        @Override
        public NativeHttp.Response send(String method, String url, Map<String, String> headers, String body) {
            sent.add(new Sent(method, url, headers, body));
            if (url.equals(ORIGIN + "/api/push/voip/status")) return response(statusCode, statusBody);
            if (url.equals(RELAY + "/v1/register")) {
                if (deliverChallenge && registerCode == 202) {
                    if (beforeChallenge != null) beforeChallenge.run();
                    registration.handleChallenge(CHALLENGE);
                }
                return response(registerCode, "{\"status\":\"sent\"}");
            }
            if (url.equals(RELAY + "/v1/confirm")) {
                JSONObject request;
                try {
                    request = new JSONObject(body);
                } catch (JSONException e) {
                    return response(400, "{}");
                }
                CallSecretStore.Secret secret = secrets.load();
                PushEnvelope.Keys keys = secret == null ? null : PushEnvelope.deriveKeys(secret.secret);
                String proof = keys == null ? null : PushEnvelope.confirmProof(request.optString("c"), keys.send);
                if (proof == null || !proof.equals(request.optString("p"))) return response(401, "{\"error\":\"unauthorized\"}");
                return response(200, "{\"handle\":\"" + HANDLE + "\",\"expires_at\":" + (NOW / 1000 + 90L * 86400)
                    + ",\"token\":\"" + confirmToken + "\",\"env\":\"" + confirmEnv + "\",\"provider\":\"fcm\",\"type\":\""
                    + confirmType + "\"}");
            }
            if (url.equals(ORIGIN + "/api/push/voip")) {
                if ("POST".equals(method) && duringServerPost != null) duringServerPost.run();
                return response("POST".equals(method) ? postCode : 204, "");
            }
            throw new AssertionError("unexpected " + method + " " + url);
        }

        List<String> lines() {
            List<String> out = new ArrayList<>();
            for (Sent s : sent) out.add(s.method + " " + s.url.replace(ORIGIN, "").replace(RELAY, "relay"));
            return out;
        }

        Sent find(String method, String url) {
            for (Sent s : sent) if (s.method.equals(method) && s.url.equals(url)) return s;
            return null;
        }
    }

    private static NativeHttp.Response response(int status, String body) {
        return new NativeHttp.Response(status, body, Collections.emptyMap());
    }

    private FakeHost host;
    private MemoryBox box;
    private CallSecretStore secrets;
    private Memory memory;
    private VoipRegistration registration;

    @Before
    public void setUp() {
        host = new FakeHost();
        box = new MemoryBox();
        secrets = new CallSecretStore(box);
        memory = new Memory();
        registration = new VoipRegistration(host, secrets, memory, 300);
    }

    // ---- registering

    @Test
    public void registersTheCallDestinationInTheDocumentedOrder() {
        registration.refresh(false);
        assertEquals(Arrays.asList(
            "GET /api/push/voip/status",
            "POST relay/v1/register",
            "POST relay/v1/confirm",
            "POST /api/push/voip"), host.lines());
        assertEquals("Moa-Device dev_1.credential", host.sent.get(0).headers.get("Authorization"));

        // The same FCM token as the alerts, but a call destination: type voip, provider fcm.
        JSONObject register = host.find("POST", RELAY + "/v1/register").json();
        assertEquals(TOKEN, register.optString("token"));
        assertEquals("production", register.optString("env"));
        assertEquals("voip", register.optString("type"));
        assertEquals("fcm", register.optString("provider"));
        CallSecretStore.Secret secret = secrets.load();
        assertNotNull(secret);
        PushEnvelope.Keys keys = PushEnvelope.deriveKeys(secret.secret);
        assertEquals(PushEnvelope.base64UrlEncode(keys.send), register.optString("send_key"));

        Sent post = host.find("POST", ORIGIN + "/api/push/voip");
        assertEquals("Moa-Device dev_1.credential", post.headers.get("Authorization"));
        assertEquals("1", post.headers.get("X-Moa-Request"));
        assertEquals("application/json", post.headers.get("Content-Type"));
        JSONObject body = post.json();
        assertEquals(RELAY, body.optString("relay_url"));
        assertEquals(HANDLE, body.optString("handle"));
        assertEquals(NOW / 1000 + 90L * 86400, body.optLong("expires_at"));
        assertEquals(PushEnvelope.base64UrlEncode(secret.secret), body.optString("secret"));
        assertEquals("production", body.optString("env"));
        assertEquals("fcm", body.optString("provider"));
        assertEquals(6, body.length());
    }

    @Test
    public void theSecretIsOwnedByThisPairingAndKeptBeforeAnythingIsRegistered() {
        host.beforeChallenge = () -> {
            CallSecretStore.Secret saved = secrets.load();
            assertNotNull("S_v must be stored before the relay is asked", saved);
            assertEquals(ORIGIN, saved.origin);
            assertEquals("dev_1", saved.deviceId);
            assertEquals(32, saved.secret.length);
        };
        registration.refresh(false);
        assertNotNull(host.find("POST", ORIGIN + "/api/push/voip"));
        assertEquals(ORIGIN, memory.get("origin"));
        assertEquals(TOKEN, memory.get("token"));
        assertEquals("production", memory.get("env"));
    }

    @Test
    public void aRenewalKeepsTheSecretOfThisPairing() {
        registration.refresh(false);
        byte[] first = secrets.load().secret;
        host.statusBody = "{\"registered\":true,\"relay_url\":\"" + RELAY + "\",\"expires_at\":" + (NOW / 1000 + 5 * 86400) + "}";
        registration.refresh(true);
        assertTrue(Arrays.equals(first, secrets.load().secret));
        assertEquals(2, count("POST /api/push/voip"));
    }

    @Test
    public void aSecretOfAnotherServerOrDeviceIsNotReused() {
        secrets.save(new CallSecretStore.Secret("https://other.example", "dev_1", new byte[32]));
        registration.refresh(false);
        CallSecretStore.Secret now = secrets.load();
        assertEquals(ORIGIN, now.origin);
        assertFalse(Arrays.equals(new byte[32], now.secret));
    }

    // ---- when not to register

    @Test
    public void registeredForThisTokenWithPlentyOfLifeLeftDoesNothingMore() {
        registration.refresh(false);
        host.sent.clear();
        host.statusBody = "{\"registered\":true,\"relay_url\":\"" + RELAY + "\",\"expires_at\":" + (NOW / 1000 + 60L * 86400) + "}";
        host.now += 3_600_001;
        registration.refresh(false);
        assertEquals(Arrays.asList("GET /api/push/voip/status"), host.lines());
    }

    @Test
    public void closeToExpiryOrANewTokenOrAnExpiredHandleRegistersAgain() {
        registration.refresh(false);
        // Under thirty days left.
        host.sent.clear();
        host.statusBody = "{\"registered\":true,\"relay_url\":\"" + RELAY + "\",\"expires_at\":" + (NOW / 1000 + 29L * 86400) + "}";
        registration.refresh(true);
        assertEquals(1, count("POST /api/push/voip"));
        // A token FCM rotated.
        host.sent.clear();
        host.statusBody = "{\"registered\":true,\"relay_url\":\"" + RELAY + "\",\"expires_at\":" + (NOW / 1000 + 60L * 86400) + "}";
        host.token = "e".repeat(152);
        host.confirmToken = host.token;
        registration.refresh(true);
        assertEquals(1, count("POST /api/push/voip"));
        // The relay said the handle expired.
        host.sent.clear();
        host.statusBody = "{\"registered\":true,\"relay_url\":\"" + RELAY + "\",\"expires_at\":" + (NOW / 1000 + 60L * 86400)
            + ",\"last\":{\"result\":\"handle_expired\"}}";
        registration.refresh(true);
        assertEquals(1, count("POST /api/push/voip"));
    }

    @Test
    public void onlyWithTheAppOnScreen() {
        host.foreground = false;
        registration.refresh(true);
        assertTrue(host.sent.isEmpty());
        assertNull(secrets.load());
    }

    @Test
    public void atMostOnceAnHourUnlessAskedAgain() {
        host.statusCode = 404;
        registration.refresh(false);
        registration.refresh(false);
        assertEquals(1, host.sent.size());
        host.now += 3_599_000;
        registration.refresh(false);
        assertEquals(1, host.sent.size());
        host.now += 2_000;
        registration.refresh(false);
        assertEquals(2, host.sent.size());
        registration.refresh(true);
        assertEquals(3, host.sent.size());
    }

    @Test
    public void anOlderServerOrOfflineDoesNothingAndCreatesNoSecret() {
        host.statusCode = 404;
        registration.refresh(false);
        assertNull(secrets.load());
        assertEquals(1, host.sent.size());
        host.statusCode = 503;
        registration.refresh(true);
        assertNull(secrets.load());
        assertEquals(2, host.sent.size());
    }

    @Test
    public void noValidCredentialOrAnotherRelayDoesNothing() {
        host.paired = null;
        registration.refresh(true);
        assertTrue(host.sent.isEmpty());
        host.paired = new DeviceCredentialStore.Stored(ORIGIN, "dev_1.credential", NOW - 1);
        registration.refresh(true);
        assertTrue(host.sent.isEmpty());
        host.paired = new DeviceCredentialStore.Stored(ORIGIN, "nodot", NOW + 86_400_000L);
        registration.refresh(true);
        assertTrue(host.sent.isEmpty());
        host.paired = new DeviceCredentialStore.Stored("http://moa.example", "dev_1.credential", NOW + 86_400_000L);
        registration.refresh(true);
        assertTrue(host.sent.isEmpty());

        host.paired = new DeviceCredentialStore.Stored(ORIGIN, "dev_1.credential", NOW + 86_400_000L);
        host.statusBody = "{\"registered\":false,\"relay_url\":\"https://elsewhere.example\"}";
        registration.refresh(true);
        assertEquals(Arrays.asList("GET /api/push/voip/status"), host.lines());
        assertNull(secrets.load());
    }

    @Test
    public void noFcmTokenMeansNoRegistration() {
        host.tokenFailure = new IOException("no firebase");
        registration.refresh(true);
        assertEquals(1, host.sent.size());
        assertNull(secrets.load());
    }

    // ---- the challenge

    @Test
    public void aChallengeWithNothingPendingIsIgnored() {
        registration.handleChallenge(CHALLENGE);
        registration.handleChallenge("not base64!");
        assertTrue(host.sent.isEmpty());
    }

    @Test
    public void aChallengeThatIsNotBase64UrlOrIsTooLongIsNeverConfirmed() {
        host.deliverChallenge = false;
        host.beforeChallenge = null;
        Thread t = new Thread(() -> registration.refresh(false));
        t.start();
        waitFor(() -> host.find("POST", RELAY + "/v1/register") != null);
        registration.handleChallenge("not base64!");
        registration.handleChallenge("a".repeat(2049));
        registration.handleChallenge("");
        assertNull(host.find("POST", RELAY + "/v1/confirm"));
        join(t);
    }

    @Test
    public void aChallengeIsAnsweredOnlyForTheTypeThatWasRegistered() {
        // An alert-type destination of this same token confirmed with the same
        // challenge shape: it is not the call destination, so it is dropped.
        host.confirmType = "alert";
        registration.refresh(false);
        assertNotNull(host.find("POST", RELAY + "/v1/confirm"));
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
        assertNull(memory.get("token"));
    }

    @Test
    public void aConfirmationWithoutATypeIsNotACallDestination() {
        host.confirmType = "";
        registration.refresh(false);
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
    }

    @Test
    public void aChallengeForAnotherTokenOrEnvIsDropped() {
        host.confirmToken = "d".repeat(152);
        registration.refresh(false);
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
        host.sent.clear();
        host.confirmToken = TOKEN;
        host.confirmEnv = "sandbox";
        registration.refresh(true);
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
    }

    @Test
    public void aChallengeSealedForAnotherSecretIsNotConfirmedAsTheOne() {
        // The relay answers 401 to a proof made with a key that is not the sealed one:
        // this is what an alert challenge looks like to the call registration.
        host.beforeChallenge = () -> secrets.save(new CallSecretStore.Secret(ORIGIN, "dev_1", new byte[32]));
        registration.refresh(false);
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
    }

    @Test
    public void aChallengeAfterTheWindowIsIgnored() {
        host.deliverChallenge = false;
        Thread t = new Thread(() -> registration.refresh(false));
        t.start();
        waitFor(() -> host.find("POST", RELAY + "/v1/register") != null);
        sleep(450);
        registration.handleChallenge(CHALLENGE);
        assertNull(host.find("POST", RELAY + "/v1/confirm"));
        join(t);
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
    }

    @Test
    public void aRelayThatRefusesToRegisterEndsItWithoutWaiting() {
        host.registerCode = 429;
        long start = System.currentTimeMillis();
        registration.refresh(false);
        assertTrue(System.currentTimeMillis() - start < 250);
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
    }

    @Test
    public void aServerThatRefusesTheDestinationSavesNoState() {
        host.postCode = 400;
        registration.refresh(false);
        assertNotNull(host.find("POST", ORIGIN + "/api/push/voip"));
        assertNull(memory.get("token"));
        // The secret stays: the next attempt reuses it.
        assertNotNull(secrets.load());
    }

    // ---- a registration that failed is redone without reopening the app (B1)

    // QA B1: the first pairing's token was refused twice by the relay (FCM
    // answered 404/400, relay 410) and the destination stayed unregistered
    // until the app was killed and reopened.
    @Test
    public void aTokenFcmRefusedIsRegisteredAgainByItselfWhenItIsAccepted() {
        host.registerCode = 410;
        // The foreground refresh and the one onNewToken queued behind it.
        registration.refresh(false);
        registration.refresh(true);
        assertEquals(2, count("POST relay/v1/register"));
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
        // Two failed runs ask for one retry, not two.
        assertEquals(1, host.retryTasks.size());
        assertEquals(15_000L, (long) host.retryDelays.get(0));

        host.registerCode = 202;
        host.retryTasks.remove(0).run();
        assertNotNull(host.find("POST", ORIGIN + "/api/push/voip"));
        assertEquals(TOKEN, memory.get("token"));
        assertTrue(host.retryTasks.isEmpty());
    }

    @Test
    public void aFailedRegistrationDoesNotBlockTheNextForegroundForAnHour() {
        host.registerCode = 410;
        registration.refresh(false);
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
        host.registerCode = 202;
        host.now += 60_000;
        registration.refresh(false);
        assertNotNull(host.find("POST", ORIGIN + "/api/push/voip"));
    }

    @Test
    public void anOfflinePhoneOrAServerThatRefusesAlsoTriesAgain() {
        host.postCode = 503;
        registration.refresh(false);
        assertEquals(1, host.retryTasks.size());
        host.postCode = 204;
        host.retryTasks.remove(0).run();
        assertEquals(TOKEN, memory.get("token"));
    }

    @Test
    public void retriesBackOffAndStopAfterThree() {
        host.registerCode = 410;
        registration.refresh(false);
        for (int i = 0; i < 5 && !host.retryTasks.isEmpty(); i++) host.retryTasks.remove(0).run();
        assertEquals(Arrays.asList(15_000L, 60_000L, 300_000L), host.retryDelays);
        assertTrue(host.retryTasks.isEmpty());
    }

    @Test
    public void aSuccessLeavesNothingToRetry() {
        registration.refresh(false);
        assertTrue(host.retryTasks.isEmpty());
        registration.refresh(true);
        assertTrue(host.retryTasks.isEmpty());
    }

    @Test
    public void nothingToRetryWhenTheServerCannotUseCalls() {
        host.statusCode = 404;
        registration.refresh(false);
        assertTrue(host.retryTasks.isEmpty());
    }

    // A token FCM rotated while the app was in the background cannot be
    // registered then (a challenge must not ring a locked phone); it is due at
    // the next foreground, not an hour later.
    @Test
    public void aTokenRotatedInTheBackgroundIsRegisteredAtTheNextForeground() {
        registration.refresh(false);
        host.sent.clear();
        host.statusBody = "{\"registered\":true,\"relay_url\":\"" + RELAY + "\",\"expires_at\":" + (NOW / 1000 + 60L * 86400) + "}";
        host.token = "e".repeat(152);
        host.confirmToken = host.token;
        host.foreground = false;
        registration.refresh(true);
        assertTrue(host.sent.isEmpty());
        host.foreground = true;
        host.now += 600_000;
        registration.refresh(false);
        assertEquals(1, count("POST /api/push/voip"));
        assertEquals(host.token, memory.get("token"));
    }

    // ---- forgetting

    @Test
    public void forgettingDeletesTheSecretAndTheStateAndRegistersFromScratchAfterwards() {
        registration.refresh(false);
        byte[] first = secrets.load().secret;
        assertTrue(registration.forgetLocal());
        assertNull(secrets.load());
        assertNull(memory.get("token"));
        host.sent.clear();
        registration.refresh(false);
        assertEquals(4, host.sent.size());
        assertFalse(Arrays.equals(first, secrets.load().secret));
    }

    @Test
    public void forgettingWhileWaitingForTheChallengeStopsTheRegistration() {
        host.beforeChallenge = () -> assertTrue(registration.forgetLocal());
        registration.refresh(false);
        assertNull(host.find("POST", ORIGIN + "/api/push/voip"));
        assertNull(secrets.load());
        assertNull(memory.get("token"));
    }

    @Test
    public void whatAForgottenRegistrationCreatedOnTheServerIsDeleted() {
        host.duringServerPost = () -> registration.forgetLocal();
        registration.refresh(false);
        Sent delete = host.find("DELETE", ORIGIN + "/api/push/voip");
        assertNotNull(delete);
        assertEquals("Moa-Device dev_1.credential", delete.headers.get("Authorization"));
        assertEquals("1", delete.headers.get("X-Moa-Request"));
        assertNull(memory.get("token"));
        assertNull(secrets.load());
    }

    @Test
    public void aSecretThatCannotBeDeletedIsReported() {
        registration.refresh(false);
        CallSecretStore failing = new CallSecretStore(new SecretBox() {
            @Override
            public byte[] load() {
                return box.load();
            }

            @Override
            public void save(byte[] plaintext) {
                box.save(plaintext);
            }

            @Override
            public void delete() throws IOException {
                throw new IOException("disk");
            }
        });
        VoipRegistration stuck = new VoipRegistration(host, failing, memory, 300);
        assertFalse(stuck.forgetLocal());
        assertNull(memory.get("token"));
    }

    @Test
    public void unregisterAsksTheServerToDropTheDestination() {
        registration.unregister(host.paired);
        Sent delete = host.find("DELETE", ORIGIN + "/api/push/voip");
        assertNotNull(delete);
        assertEquals("Moa-Device dev_1.credential", delete.headers.get("Authorization"));
        assertEquals("1", delete.headers.get("X-Moa-Request"));
        assertNull(delete.body);
    }

    // ---- helpers

    private int count(String line) {
        int n = 0;
        for (String l : host.lines()) if (l.equals(line)) n++;
        return n;
    }

    private static void waitFor(java.util.function.BooleanSupplier condition) {
        long deadline = System.currentTimeMillis() + 3000;
        while (!condition.getAsBoolean()) {
            if (System.currentTimeMillis() > deadline) throw new AssertionError("timed out");
            sleep(10);
        }
    }

    private static void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private static void join(Thread t) {
        try {
            t.join(3000);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        assertFalse("registration still waiting", t.isAlive());
    }
}

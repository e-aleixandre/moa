package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;
import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;

// Review A2: an incoming message that passed the receiving gate must not post
// after a disable finished forgetting S/state and cancelling every
// notification. The receive below follows PushNotifier.show step by step
// (generation, gate, key read, open, post) with the real PushLifecycle and
// real envelope crypto; the forget is NativePushRegistration.forgetLocal's
// (S, STATE_ORIGIN and cancelAll inside lifecycle.end).
public class PushLifecycleTest {

    private enum Pause { BEFORE_KEY_READ, BEFORE_POST }

    private final PushLifecycle lifecycle = new PushLifecycle();
    private final AtomicReference<byte[]> secret = new AtomicReference<>();
    private volatile boolean on;
    private final List<String> shown = Collections.synchronizedList(new ArrayList<>());
    private Map<String, String> sealed;
    private long now;

    @Before
    public void enable() throws Exception {
        String root = System.getProperty("moa.repoRoot");
        Path path = Paths.get(root == null ? "../../.." : root, "relay", "test", "vectors.json");
        JSONObject vectors = new JSONObject(new String(Files.readAllBytes(path), StandardCharsets.UTF_8));
        JSONObject e = vectors.getJSONObject("envelope").getJSONObject("envelope");
        sealed = new HashMap<>();
        sealed.put("v", String.valueOf(e.getInt("v")));
        sealed.put("k", e.getString("k"));
        sealed.put("c", e.getString("c"));
        now = vectors.getJSONObject("envelope").getLong("now");
        secret.set(PushEnvelope.base64UrlDecode(vectors.getJSONObject("device").getString("S")));
        on = true;
    }

    private static Map<String, String> malformed() {
        Map<String, String> data = new HashMap<>();
        data.put("v", "1");
        data.put("k", "bad");
        data.put("c", "bad");
        return data;
    }

    private void forget() {
        lifecycle.end(() -> {
            secret.set(null);
            on = false;
            shown.clear();
            return true;
        });
    }

    // PushNotifier.show; the post records "private:<channel>" or "generic:<channel>".
    private void receive(Map<String, String> data, Pause pause, CountDownLatch paused, CountDownLatch resume)
        throws InterruptedException {
        int generation = lifecycle.generation();
        if (!on) return;
        if (pause == Pause.BEFORE_KEY_READ) hold(paused, resume);
        PushEnvelope.Keys keys = PushEnvelope.deriveKeys(secret.get());
        PushEnvelope.Content content = PushEnvelope.open(data, keys, now);
        String posted = content == null
            ? "generic:" + PushMessages.fallbackChannel()
            : "private:" + PushMessages.channelFor(content.level);
        if (pause == Pause.BEFORE_POST) hold(paused, resume);
        lifecycle.publish(generation, () -> on, () -> shown.add(posted));
    }

    private static void hold(CountDownLatch paused, CountDownLatch resume) throws InterruptedException {
        paused.countDown();
        assertTrue("receive was never resumed", resume.await(5, TimeUnit.SECONDS));
    }

    // Receive passes the gate and pauses; disable completes; receive resumes.
    private void raceAgainstDisable(Map<String, String> data, Pause pause) throws Exception {
        CountDownLatch paused = new CountDownLatch(1);
        CountDownLatch resume = new CountDownLatch(1);
        AtomicReference<Throwable> failure = new AtomicReference<>();
        Thread incoming = new Thread(() -> {
            try {
                receive(data, pause, paused, resume);
            } catch (Throwable e) {
                failure.set(e);
            }
        });
        incoming.start();
        assertTrue(paused.await(5, TimeUnit.SECONDS));
        forget();
        assertNull(secret.get());
        assertFalse(on);
        assertTrue(shown.isEmpty());
        resume.countDown();
        incoming.join(5000);
        assertFalse(incoming.isAlive());
        assertNull(failure.get());
        assertEquals("posted after disable completed", Collections.emptyList(), shown);
    }

    @Test
    public void aPrivateMessageDoesNotPostAfterDisable() throws Exception {
        raceAgainstDisable(sealed, Pause.BEFORE_POST);
    }

    @Test
    public void aMalformedMessageDoesNotPostTheFallbackAfterDisable() throws Exception {
        raceAgainstDisable(malformed(), Pause.BEFORE_POST);
    }

    @Test
    public void aMessageWhoseKeyIsReadAfterDisableDoesNotPostTheFallback() throws Exception {
        raceAgainstDisable(sealed, Pause.BEFORE_KEY_READ);
    }

    // Push really on: the private message posts, and an unreadable one still
    // gets the sounding generic alert.
    @Test
    public void anEnabledDevicePostsPrivateAndFallbackAlerts() throws Exception {
        receive(sealed, null, null, null);
        receive(malformed(), null, null, null);
        assertEquals(2, shown.size());
        assertTrue(shown.get(0).startsWith("private:"));
        assertEquals("generic:" + PushMessages.CHANNEL_ACTIVE, shown.get(1));
        assertNotNull(secret.get());
    }

    // A post that landed before the forget is cancelled by it.
    @Test
    public void aPostBeforeDisableIsCancelledByIt() throws Exception {
        receive(sealed, null, null, null);
        assertEquals(1, shown.size());
        forget();
        assertTrue(shown.isEmpty());
    }
}

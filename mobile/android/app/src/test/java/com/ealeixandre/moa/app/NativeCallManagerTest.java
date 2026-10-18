package com.ealeixandre.moa.app;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.Before;
import org.junit.Test;

// The single native call's state machine against fakes of every platform
// edge (socket, audio, system call, page). The contract is iOS
// NativeVoiceCallManager's, which has no tests of its own.
public class NativeCallManagerTest {

    static final String ORIGIN = "https://moa.example.ts.net:8443";
    static final String URL = "wss://moa.example.ts.net:8443/api/voice/calls/call_1/ws";
    static final String URL2 = "wss://moa.example.ts.net:8443/api/voice/calls/call_2/ws";

    static final class Event {
        final String id;
        final String type;
        final Object data;

        Event(String id, String type, Object data) {
            this.id = id;
            this.type = type;
            this.data = data;
        }

        @Override
        public String toString() {
            return id + ":" + type + ":" + data;
        }
    }

    static final class FakeSocket implements NativeCallManager.Socket {
        final String url;
        final String authorization;
        final NativeCallManager.SocketEvents events;
        final List<String> texts = new ArrayList<>();
        final List<byte[]> binaries = new ArrayList<>();
        long queue;
        int closeCode = -1;
        String closeReason;

        FakeSocket(String url, String authorization, NativeCallManager.SocketEvents events) {
            this.url = url;
            this.authorization = authorization;
            this.events = events;
        }

        @Override
        public void sendText(String text) {
            texts.add(text);
        }

        @Override
        public boolean sendBinary(byte[] frame) {
            binaries.add(frame);
            return true;
        }

        @Override
        public long queueSize() {
            return queue;
        }

        @Override
        public void close(int code, String reason) {
            closeCode = code;
            closeReason = reason;
        }
    }

    static final class FakeAudio implements NativeCallManager.Audio {
        final NativeCallManager.AudioEvents events;
        final List<String> log = new ArrayList<>();
        final List<byte[]> played = new ArrayList<>();
        Exception startFailure;
        boolean muted;

        FakeAudio(NativeCallManager.AudioEvents events) {
            this.events = events;
        }

        @Override
        public void start() throws Exception {
            log.add("start");
            if (startFailure != null) throw startFailure;
        }

        @Override
        public void stop() {
            log.add("stop");
        }

        @Override
        public void play(byte[] pcm) {
            played.add(pcm);
        }

        @Override
        public void interrupt() {
            log.add("interrupt");
        }

        @Override
        public void setMuted(boolean m) {
            muted = m;
            log.add("muted=" + m);
        }
    }

    static final class FakeSystem implements NativeCallManager.SystemCall {
        final List<String> log = new ArrayList<>();
        NativeCallManager.SystemListener listener;
        Exception microphoneFailure;

        @Override
        public void start(String name, NativeCallManager.SystemListener l) {
            log.add("start " + name);
            listener = l;
        }

        @Override
        public void connected() {
            log.add("connected");
        }

        @Override
        public void enableMicrophone() throws Exception {
            log.add("microphone");
            if (microphoneFailure != null) throw microphoneFailure;
        }

        @Override
        public void end(boolean failed) {
            log.add(failed ? "end failed" : "end");
        }
    }

    static final class Task {
        final long at;
        final Runnable run;
        boolean cancelled;

        Task(long at, Runnable run) {
            this.at = at;
            this.run = run;
        }
    }

    static final class FakeHost implements NativeCallManager.Host {
        String origin = ORIGIN;
        String credential = "dev_1.secret";
        String mic = "granted";
        Boolean micAnswer = true;
        NativeCallManager.MicResult pendingMic;
        long now;
        final List<Event> events = new ArrayList<>();
        final List<FakeSocket> sockets = new ArrayList<>();
        final List<FakeAudio> audios = new ArrayList<>();
        final FakeSystem system = new FakeSystem();
        final List<Task> tasks = new ArrayList<>();
        Exception audioStartFailure;

        @Override
        public String pairedOrigin() {
            return origin;
        }

        @Override
        public String credential(String forOrigin) {
            return forOrigin != null && forOrigin.equals(origin) ? credential : null;
        }

        @Override
        public NativeCallManager.Socket connect(String url, String authorization, NativeCallManager.SocketEvents e) {
            FakeSocket socket = new FakeSocket(url, authorization, e);
            sockets.add(socket);
            return socket;
        }

        @Override
        public NativeCallManager.Audio createAudio(NativeCallManager.AudioEvents e) {
            FakeAudio audio = new FakeAudio(e);
            audio.startFailure = audioStartFailure;
            audios.add(audio);
            return audio;
        }

        @Override
        public NativeCallManager.SystemCall systemCall() {
            return system;
        }

        @Override
        public String micState() {
            return mic;
        }

        @Override
        public void requestMic(NativeCallManager.MicResult result) {
            if (micAnswer == null) pendingMic = result;
            else result.granted(micAnswer);
        }

        @Override
        public void emit(String id, String type, Object data) {
            events.add(new Event(id, type, data));
        }

        @Override
        public Runnable schedule(Runnable task, long delayMillis) {
            Task t = new Task(now + delayMillis, task);
            tasks.add(t);
            return () -> t.cancelled = true;
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

        List<Event> of(String type) {
            List<Event> out = new ArrayList<>();
            for (Event e : events) if (e.type.equals(type)) out.add(e);
            return out;
        }

        Event last(String type) {
            List<Event> all = of(type);
            return all.isEmpty() ? null : all.get(all.size() - 1);
        }

        FakeSocket socket() {
            return sockets.get(sockets.size() - 1);
        }

        FakeAudio audio() {
            return audios.get(audios.size() - 1);
        }
    }

    private FakeHost host;
    private NativeCallManager manager;

    @Before
    public void setUp() {
        host = new FakeHost();
        manager = new NativeCallManager(host);
    }

    private void open(String id) {
        manager.open(id, URL, "Moa", "sess_1");
    }

    private void opened(String id) {
        open(id);
        host.socket().events.opened();
    }

    // A call that is open, with the system call ready and the audio running.
    private FakeAudio running(String id) {
        opened(id);
        host.system.listener.ready();
        manager.startAudio(id);
        return host.audios.isEmpty() ? null : host.audio();
    }

    private static byte[] pcm(int n) {
        byte[] b = new byte[n];
        Arrays.fill(b, (byte) 7);
        return b;
    }

    // ---- open

    @Test
    public void refusesABadUrlWithoutOpeningAnything() {
        manager.open("n1", "wss://evil.example/api/voice/calls/c/ws", "Moa", "s");
        assertTrue(host.sockets.isEmpty());
        assertEquals("n1:close:null", host.events.get(0).toString());
        assertTrue(host.system.log.isEmpty());
    }

    @Test
    public void refusesWhenUnpairedOrWithoutAValidCredential() {
        host.origin = null;
        open("n1");
        host.origin = ORIGIN;
        host.credential = null;
        open("n2");
        assertTrue(host.sockets.isEmpty());
        assertEquals(2, host.of("close").size());
        assertFalse(manager.hasCall());
    }

    @Test
    public void connectsWithTheDeviceCredentialAndRegistersTheSystemCall() {
        manager.open("n1", URL, "M" + "o".repeat(200), "s".repeat(300));
        assertEquals(URL, host.socket().url);
        assertEquals("Moa-Device dev_1.secret", host.socket().authorization);
        assertEquals("start M" + "o".repeat(79), host.system.log.get(0));
        assertTrue(manager.hasCall());
        assertTrue(host.events.isEmpty());
    }

    @Test
    public void aNewOpenEndsTheRunningCallAsReplaced() {
        opened("n1");
        FakeSocket first = host.socket();
        manager.open("n2", URL2, "Moa", "sess_2");
        assertEquals(1000, first.closeCode);
        assertEquals("native:replaced mic=granted", first.closeReason);
        assertEquals("n1:close:native:replaced mic=granted", host.last("close").toString());
        assertEquals(URL2, host.socket().url);
        // The old socket reporting afterwards changes nothing.
        first.events.closed(1000);
        first.events.failed("late");
        assertEquals(1, host.of("close").size());
        assertTrue(manager.hasCall());
    }

    @Test
    public void socketOpenIsToldToThePageAndTheSystem() {
        opened("n1");
        assertEquals("n1:open:null", host.last("open").toString());
        assertTrue(host.system.log.contains("connected"));
    }

    // ---- server messages

    @Test
    public void mirrorsEveryServerTextToThePage() {
        opened("n1");
        host.socket().events.text("{\"type\":\"state\",\"state\":\"listening\"}");
        host.socket().events.text("{\"type\":\"pong\"}");
        assertEquals(2, host.of("message").size());
        assertEquals("{\"type\":\"pong\"}", host.last("message").data);
    }

    @Test
    public void bargeInClearsThePlaybackAndHeldAudio() {
        FakeAudio audio = running("n1");
        host.socket().events.text("{\"type\":\"speech_started\"}");
        assertTrue(audio.log.contains("interrupt"));
        audio.log.clear();
        host.socket().events.text("{\"type\":\"state\",\"state\":\"speaking\",\"interrupt\":true}");
        assertEquals(List.of("interrupt"), audio.log);
        host.socket().events.text("{\"type\":\"state\",\"state\":\"speaking\"}");
        assertEquals(List.of("interrupt"), audio.log);
    }

    @Test
    public void audioBeforeTheSpeakerIsHeldUpToFourSecondsAndPlayedWhenItStarts() {
        opened("n1");
        byte[] first = pcm(1000);
        host.socket().events.binary(first);
        // Four seconds are 192000 bytes: older audio is dropped first.
        for (int i = 0; i < 200; i++) host.socket().events.binary(pcm(1000));
        host.system.listener.ready();
        manager.startAudio("n1");
        long total = 0;
        for (byte[] chunk : host.audio().played) total += chunk.length;
        assertTrue(total <= CallProtocol.PENDING_MAX_BYTES);
        assertTrue(total > CallProtocol.PENDING_MAX_BYTES - 1000);
        assertFalse(host.audio().played.contains(first));
    }

    @Test
    public void bargeInDropsAudioHeldBeforeTheSpeaker() {
        opened("n1");
        host.socket().events.binary(pcm(1000));
        host.socket().events.text("{\"type\":\"speech_started\"}");
        host.system.listener.ready();
        manager.startAudio("n1");
        assertTrue(host.audio().played.isEmpty());
    }

    @Test
    public void playsServerAudioStraightAwayOnceTheSpeakerIsOn() {
        FakeAudio audio = running("n1");
        byte[] chunk = pcm(480);
        host.socket().events.binary(chunk);
        assertEquals(1, audio.played.size());
        assertArrayEquals(chunk, audio.played.get(0));
    }

    @Test
    public void forwardsPlaybackEventsToThePage() {
        FakeAudio audio = running("n1");
        audio.events.playback("playing");
        audio.events.playback("drained");
        assertEquals("n1:playback:playing", host.of("playback").get(0).toString());
        assertEquals("drained", host.of("playback").get(1).data);
    }

    // ---- audio

    @Test
    public void startAudioForAnUnknownCallSaysMicrophone() {
        manager.startAudio("nope");
        assertEquals("nope:audio:microphone", host.last("audio").toString());
        open("n1");
        manager.startAudio("other");
        assertEquals("other:audio:microphone", host.last("audio").toString());
        assertTrue(host.audios.isEmpty());
    }

    @Test
    public void startAudioStartsTheMicrophoneServiceThenTheEngineAndReportsOk() {
        FakeAudio audio = running("n1");
        assertEquals(List.of("start Moa", "connected", "microphone"), host.system.log);
        assertEquals(List.of("start"), audio.log);
        assertEquals("n1:audio:ok", host.last("audio").toString());
    }

    @Test
    public void startAudioWaitsForTheSystemCall() {
        opened("n1");
        manager.startAudio("n1");
        assertTrue(host.audios.isEmpty());
        assertTrue(host.of("audio").isEmpty());
        host.system.listener.ready();
        assertEquals(1, host.audios.size());
        assertEquals("n1:audio:ok", host.last("audio").toString());
    }

    @Test
    public void startAudioWaitsForThePermissionAnswer() {
        host.micAnswer = null;
        opened("n1");
        host.system.listener.ready();
        manager.startAudio("n1");
        assertTrue(host.audios.isEmpty());
        host.pendingMic.granted(true);
        assertEquals(1, host.audios.size());
    }

    @Test
    public void deniedMicrophoneEndsTheCallWithItsReason() {
        host.mic = "denied";
        host.micAnswer = false;
        opened("n1");
        host.system.listener.ready();
        manager.startAudio("n1");
        assertEquals("n1:audio:microphone", host.last("audio").toString());
        assertEquals("native:mic_denied mic=denied", host.socket().closeReason);
        assertTrue(host.audios.isEmpty());
        assertFalse(manager.hasCall());
    }

    @Test
    public void aSecondStartAudioFromAnAdoptingPageJustConfirms() {
        running("n1");
        host.events.clear();
        manager.startAudio("n1");
        assertEquals(1, host.audios.size());
        assertEquals("n1:audio:ok", host.last("audio").toString());
    }

    @Test
    public void anEngineThatCannotStartLetsThePageTakeOver() {
        host.audioStartFailure = new IllegalStateException("no input");
        running("n1");
        assertEquals("n1:audio:audio_failed", host.last("audio").toString());
        assertTrue(host.socket().closeReason.startsWith("native:engine IllegalStateException"));
        assertFalse(manager.hasCall());
        assertEquals("n1", host.last("close").id);
    }

    @Test
    public void aMicrophoneServiceTheSystemRefusesIsAnAudioFailureNotAPermissionOne() {
        host.system.microphoneFailure = new SecurityException("fgs");
        running("n1");
        assertEquals("n1:audio:audio_failed", host.last("audio").toString());
        assertTrue(host.audios.isEmpty());
        assertTrue(host.socket().closeReason.startsWith("native:engine SecurityException"));
    }

    @Test
    public void audioLostAfterARouteChangeEndsTheCall() {
        FakeAudio audio = running("n1");
        audio.events.failed();
        assertTrue(host.socket().closeReason.startsWith("native:route_rebuild"));
        assertFalse(manager.hasCall());
    }

    @Test
    public void systemCallThatCannotBeSetUpEndsAsAnAudioFailure() {
        opened("n1");
        host.system.listener.failed("telecom_refused");
        assertEquals("n1:audio:audio_failed", host.last("audio").toString());
        assertTrue(host.socket().closeReason.startsWith("native:system telecom_refused"));
    }

    @Test
    public void stopAudioSilencesTheEngineAndAllowsRestarting() {
        FakeAudio audio = running("n1");
        manager.stopAudio("n1");
        assertEquals("stop", audio.log.get(audio.log.size() - 1));
        manager.stopAudio("n1");
        manager.startAudio("n1");
        assertEquals(2, host.audios.size());
    }

    // ---- capture frames

    @Test
    public void sendsCapturedFramesWhileOpen() {
        FakeAudio audio = running("n1");
        byte[] frame = pcm(960);
        audio.events.frame(frame);
        assertEquals(1, host.socket().binaries.size());
        assertArrayEquals(frame, host.socket().binaries.get(0));
    }

    @Test
    public void dropsFreshFramesWhileASecondIsStillInFlight() {
        FakeAudio audio = running("n1");
        host.socket().queue = 47040;
        audio.events.frame(pcm(960));
        assertEquals(1, host.socket().binaries.size());
        host.socket().queue = 47041;
        audio.events.frame(pcm(960));
        assertEquals(1, host.socket().binaries.size());
    }

    @Test
    public void mutedCallSendsNoFramesAndTellsTheServer() {
        FakeAudio audio = running("n1");
        manager.setMutedFromPage("n1", true);
        assertEquals(List.of("{\"type\":\"mute\",\"muted\":true}"), host.socket().texts);
        assertTrue(audio.muted);
        audio.events.frame(pcm(960));
        assertTrue(host.socket().binaries.isEmpty());
        // The page asked: it is not echoed back to it.
        assertTrue(host.of("mute").isEmpty());
        manager.setMutedFromPage("n1", false);
        audio.events.frame(pcm(960));
        assertEquals(1, host.socket().binaries.size());
    }

    @Test
    public void muteFromTheSystemIsAppliedToldToTheServerAndEchoedToThePage() {
        FakeAudio audio = running("n1");
        host.system.listener.muted(true);
        assertTrue(audio.muted);
        assertEquals("{\"type\":\"mute\",\"muted\":true}", host.socket().texts.get(0));
        assertEquals("n1:mute:true", host.last("mute").toString());
    }

    @Test
    public void aMuteBeforeTheSocketOpensIsKeptButNotSent() {
        open("n1");
        manager.setMutedFromPage("n1", true);
        assertTrue(host.socket().texts.isEmpty());
        host.socket().events.opened();
        host.system.listener.ready();
        manager.startAudio("n1");
        assertTrue(host.audio().muted);
    }

    // ---- page controls

    @Test
    public void sendsPageControlsOnlyForTheOpenCall() {
        open("n1");
        manager.send("n1", "{\"type\":\"ping\"}");
        assertTrue(host.socket().texts.isEmpty());
        host.socket().events.opened();
        manager.send("other", "{\"type\":\"ping\"}");
        assertTrue(host.socket().texts.isEmpty());
        manager.send("n1", "{\"type\":\"ping\"}");
        assertEquals(List.of("{\"type\":\"ping\"}"), host.socket().texts);
    }

    @Test
    public void pageCloseEndsTheCallWithoutAnEventBack() {
        opened("n1");
        host.events.clear();
        manager.closeFromPage("n1");
        assertEquals(1000, host.socket().closeCode);
        assertEquals("native:page_closed mic=granted", host.socket().closeReason);
        assertTrue(host.of("close").isEmpty());
        assertEquals("end", host.system.log.get(host.system.log.size() - 1));
        assertFalse(manager.hasCall());
    }

    @Test
    public void pageCloseOfAnotherIdChangesNothing() {
        opened("n1");
        manager.closeFromPage("n2");
        assertTrue(manager.hasCall());
    }

    // ---- the server ends it

    @Test
    public void serverEndedThenClosedIsACleanRemoteEnd() {
        FakeAudio audio = running("n1");
        host.socket().events.text("{\"type\":\"ended\",\"reason\":\"hangup\"}");
        host.socket().events.closed(1000);
        assertEquals("end", host.system.log.get(host.system.log.size() - 1));
        assertEquals("n1:close:native:server_closed 1000 mic=granted", host.last("close").toString());
        assertEquals("stop", audio.log.get(audio.log.size() - 1));
        assertFalse(manager.hasCall());
    }

    @Test
    public void aCloseWithoutEndedIsAFailure() {
        opened("n1");
        host.socket().events.closed(1006);
        assertEquals("end failed", host.system.log.get(host.system.log.size() - 1));
        assertEquals("n1:close:native:server_closed 1006 mic=granted", host.last("close").toString());
    }

    @Test
    public void aTransportFailureEndsTheCallWithoutReconnecting() {
        opened("n1");
        host.socket().events.failed("SocketTimeoutException");
        assertEquals("n1:close:native:socket SocketTimeoutException mic=granted", host.last("close").toString());
        assertEquals(1, host.sockets.size());
        assertFalse(manager.hasCall());
    }

    @Test
    public void finishingTwiceReportsOnce() {
        opened("n1");
        host.socket().events.closed(1000);
        host.socket().events.failed("x");
        host.socket().events.closed(1001);
        assertEquals(1, host.of("close").size());
        assertEquals(1, host.system.log.stream().filter(s -> s.startsWith("end")).count());
    }

    // ---- hang up from outside

    @Test
    public void hangUpSilencesAudioAsksTheServerAndWaitsForItsEnd() {
        FakeAudio audio = running("n1");
        host.system.listener.hangUp();
        assertEquals("stop", audio.log.get(audio.log.size() - 1));
        assertEquals(List.of("{\"type\":\"hangup\"}"), host.socket().texts);
        assertTrue(manager.hasCall());
        host.advance(4999);
        assertTrue(manager.hasCall());
        // The server ends it and closes: the timer is moot.
        host.socket().events.text("{\"type\":\"ended\",\"reason\":\"hangup\"}");
        host.socket().events.closed(1000);
        assertFalse(manager.hasCall());
        assertEquals("end", host.system.log.get(host.system.log.size() - 1));
        host.advance(10000);
        assertEquals(1, host.of("close").size());
    }

    @Test
    public void hangUpGivesUpAfterFiveSeconds() {
        running("n1");
        host.system.listener.hangUp();
        host.advance(5000);
        assertFalse(manager.hasCall());
        assertEquals("n1:close:native:system_hangup_timeout mic=granted", host.last("close").toString());
        // Remote ended on purpose: not reported to the system as failed.
        assertEquals("end", host.system.log.get(host.system.log.size() - 1));
    }

    @Test
    public void hangUpBeforeTheSocketOpensEndsAtOnce() {
        open("n1");
        host.system.listener.hangUp();
        assertFalse(manager.hasCall());
        assertTrue(host.socket().texts.isEmpty());
        assertEquals("native:system_hangup_before_open mic=granted", host.socket().closeReason);
    }

    @Test
    public void hangUpFromAStaleSystemListenerChangesNothing() {
        open("n1");
        NativeCallManager.SystemListener stale = host.system.listener;
        manager.open("n2", URL2, "Moa", "s");
        stale.hangUp();
        assertTrue(manager.hasCall());
        assertTrue(host.socket().texts.isEmpty());
    }

    // ---- pairing and page snapshot

    @Test
    public void unpairingEndsTheCallWithoutTellingThePage() {
        opened("n1");
        host.events.clear();
        host.origin = null;
        manager.bindingChanged();
        assertEquals("native:unpaired mic=granted", host.socket().closeReason);
        assertTrue(host.of("close").isEmpty());
        assertFalse(manager.hasCall());
    }

    @Test
    public void pairingElsewhereEndsTheCallToo() {
        opened("n1");
        host.origin = "https://other.example";
        manager.bindingChanged();
        assertFalse(manager.hasCall());
    }

    @Test
    public void aBindingThatDidNotChangeLeavesTheCallAlone() {
        opened("n1");
        manager.bindingChanged();
        assertTrue(manager.hasCall());
    }

    @Test
    public void nothingReachesAPageOfAnotherServer() {
        opened("n1");
        host.events.clear();
        host.origin = "https://other.example";
        host.socket().events.text("{\"type\":\"pong\"}");
        assertTrue(host.events.isEmpty());
        // And the call is not the new server's to control.
        manager.send("n1", "{\"type\":\"ping\"}");
        assertTrue(host.socket().texts.isEmpty());
    }

    @Test
    public void activeAnswersNullWithoutACall() {
        manager.reportActive();
        assertEquals("" + ":active:null", host.last("active").toString());
    }

    @SuppressWarnings("unchecked")
    @Test
    public void activeDescribesTheRunningCallForAnAdoptingPage() {
        opened("n1");
        manager.setMutedFromPage("n1", true);
        manager.reportActive();
        Event e = host.last("active");
        assertEquals("", e.id);
        Map<String, Object> snapshot = (Map<String, Object>) e.data;
        Map<String, Object> want = new HashMap<>();
        want.put("id", "n1");
        want.put("sessionId", "sess_1");
        want.put("callId", "call_1");
        want.put("muted", true);
        assertEquals(want, snapshot);
    }

    @Test
    public void activeIsNullOnceTheCallEndedOrBelongsToAnotherServer() {
        opened("n1");
        host.origin = "https://other.example";
        manager.reportActive();
        assertNull(host.last("active").data);
        host.origin = ORIGIN;
        manager.closeFromPage("n1");
        manager.reportActive();
        assertNull(host.last("active").data);
        assertNotNull(host.last("active"));
    }
}

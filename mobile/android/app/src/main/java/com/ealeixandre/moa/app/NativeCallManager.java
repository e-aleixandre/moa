package com.ealeixandre.moa.app;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

// The one voice call the app carries, the Android port of iOS
// NativeVoiceCallManager: it owns the call's WebSocket and audio so they go
// on with the screen locked, while the page only mirrors the JSON control
// frames. Pure Java over Host, which is every platform edge (OkHttp,
// AudioRecord/AudioTrack, Telecom and the foreground service, the page), so
// the whole life of a call is pinned by JVM tests.
//
// Threading: every method here and every callback a Host delivers runs on the
// host's single call thread, except AudioEvents.frame, which runs on the
// capture thread and touches only volatile state.
final class NativeCallManager {

    interface Socket {
        void sendText(String text);

        boolean sendBinary(byte[] frame);

        // Bytes accepted but not yet on the wire.
        long queueSize();

        // A graceful close; the adapter forces it if the server never answers.
        void close(int code, String reason);
    }

    interface SocketEvents {
        void opened();

        void text(String text);

        void binary(byte[] data);

        // The server closed the socket.
        void closed(int code);

        // The transport failed; code names the exception, never its message.
        void failed(String code);
    }

    interface Audio {
        void start() throws Exception;

        void stop();

        void play(byte[] pcm);

        // Barge-in: drop everything queued to play.
        void interrupt();

        void setMuted(boolean muted);

        // The sound moved to another device (headset, car, loudspeaker):
        // rebuild input and output on it. Anything queued to play is dropped.
        void rebuild() throws Exception;
    }

    interface AudioEvents {
        // A 960-byte captured frame. Capture thread.
        void frame(byte[] frame);

        // "playing" | "drained"
        void playback(String type);

        // The output or input could not be rebuilt after a route change.
        void failed();
    }

    // What the system (Telecom and the foreground service) says about the call.
    interface SystemListener {
        // Audio may run: the call is registered, or there is no system call
        // and the app set the audio up itself.
        void ready();

        // The system call could not be set up at all.
        void failed(String code);

        // Hung up from the notification, the lock screen, a headset or the car.
        void hangUp();

        // Muted from outside the app.
        void muted(boolean muted);

        // The sound moved to another device.
        void routeChanged();

        // Another app took the audio and there is no system call to share it
        // with: the call ends instead of hanging silent.
        void interrupted();
    }

    interface SystemCall {
        void start(String name, SystemListener listener);

        // The call of an answered invitation: Telecom already holds it ringing
        // (or refused it), so it is promoted, not placed.
        void startIncoming(String name, SystemListener listener);

        // The outgoing call's socket is up.
        void connected();

        // Allows the microphone; throws when the system refuses it.
        void enableMicrophone() throws Exception;

        // The call is over. failed: the server or the network ended it.
        void end(boolean failed);
    }

    interface MicResult {
        void granted(boolean granted);
    }

    interface Host {
        // The paired server's origin, or null when unpaired.
        String pairedOrigin();

        // A usable Moa-Device credential for that origin, or null.
        String credential(String origin);

        Socket connect(String url, String authorization, SocketEvents events);

        Audio createAudio(AudioEvents events);

        SystemCall systemCall();

        // POST /api/voice/calls/{id}/hangup with the call's own credential:
        // a call the app cannot carry must not be left to time out.
        void hangupCall(String origin, String callId, String authorization);

        // "granted" | "denied" | "undetermined", for the close reason.
        String micState();

        void requestMic(MicResult result);

        // To the page, if one that may hear it is showing. data: null, a
        // String, a Boolean or a Map of those.
        void emit(String id, String type, Object data);

        // Runs task on the call thread later; the result cancels it.
        Runnable schedule(Runnable task, long delayMillis);
    }

    private static final class Call {
        final String id;
        final String name;
        final String sessionId;
        final String callId;
        final String origin;
        final String authorization;
        // An answered invitation, with no page behind it.
        boolean incoming;
        boolean micMissing;
        volatile Socket socket;
        Audio audio;
        boolean audioWanted;
        boolean micGranted;
        boolean sessionActive;
        boolean pageClosed;
        boolean serverEnded;
        // Terminal for the microphone: once set, no document may reopen it.
        volatile boolean hangingUp;
        // The pairing the call was placed under; a new claim on the same
        // server is another device and must not inherit it.
        String deviceId;
        Runnable hangupTimer;
        final List<byte[]> pending = new ArrayList<>();
        int pendingBytes;
        volatile boolean muted;
        volatile boolean open;
        volatile boolean finished;

        Call(String id, String name, String sessionId, String callId, String origin, String authorization) {
            this.authorization = authorization;
            this.id = id;
            this.name = name;
            this.sessionId = sessionId;
            this.callId = callId;
            this.origin = origin;
        }
    }

    private final Host host;
    private volatile Call call;

    NativeCallManager(Host host) {
        this.host = host;
    }

    // ---- Operations of the page (window.MoaNativeCall)

    boolean hasCall() {
        Call c = call;
        return c != null && !c.finished;
    }

    void open(String id, String rawUrl, String name, String sessionId) {
        if (call != null) finish(call, false, "replaced");
        String origin = host.pairedOrigin();
        CallUrl url = CallUrl.validate(rawUrl, origin);
        String credential = url == null ? null : host.credential(origin);
        if (url == null || credential == null) {
            host.emit(id, "close", null);
            return;
        }
        Call c = new Call(id, CallProtocol.prefix(name, 80), CallProtocol.prefix(sessionId, 128), url.callId, origin,
            "Moa-Device " + credential);
        c.deviceId = deviceIdOf(c.authorization);
        try {
            c.socket = host.connect(url.url, "Moa-Device " + credential, new Sockets(c));
        } catch (RuntimeException e) {
            host.emit(id, "close", null);
            return;
        }
        call = c;
        host.systemCall().start(c.name, new SystemEvents(c));
    }

    // The call of an answered invitation (iOS startIncoming): the server opened
    // it for the answer, the socket joins it with the invitation's Moa-Voice
    // key, and the microphone opens by itself once the system call is ready:
    // there is no page to ask for it, and the screen may be locked. False when
    // it cannot be joined (another call, another server, a bad URL).
    boolean openIncoming(String name, String sessionId, String callId, String origin, String rawUrl, String authorization) {
        if (hasCall() || origin == null || !origin.equals(host.pairedOrigin())) return false;
        CallUrl url = CallUrl.validate(rawUrl, origin);
        if (url == null || !url.callId.equals(callId)) return false;
        Call c = new Call("i" + callId, CallProtocol.prefix(name, 80), CallProtocol.prefix(sessionId, 128), callId, origin,
            authorization);
        c.incoming = true;
        c.deviceId = deviceIdOf(authorization);
        c.audioWanted = true;
        // It cannot be asked for on a locked screen: a phone that never
        // allowed it hangs up, and says why.
        c.micGranted = host.micState().equals("granted");
        c.micMissing = !c.micGranted;
        try {
            c.socket = host.connect(url.url, authorization, new Sockets(c));
        } catch (RuntimeException e) {
            return false;
        }
        call = c;
        host.systemCall().startIncoming(c.name, new SystemEvents(c));
        return true;
    }

    // Answers a new document's question: is a call still on?
    void reportActive() {
        Call c = call;
        Object snapshot = null;
        // A call being hung up is not offered to a new document to adopt.
        if (c != null && !c.finished && !c.hangingUp && c.origin.equals(host.pairedOrigin())) {
            Map<String, Object> map = new HashMap<>();
            map.put("id", c.id);
            map.put("sessionId", c.sessionId);
            map.put("callId", c.callId);
            map.put("muted", c.muted);
            snapshot = map;
        }
        host.emit("", "active", snapshot);
    }

    void send(String id, String text) {
        Call c = current(id);
        if (c == null || !c.open || text == null) return;
        c.socket.sendText(text);
    }

    void closeFromPage(String id) {
        Call c = current(id);
        if (c == null) return;
        c.pageClosed = true;
        finish(c, false, "page_closed");
    }

    void startAudio(String id) {
        Call c = current(id);
        if (c == null || c.hangingUp) {
            host.emit(id, "audio", "microphone");
            return;
        }
        if (c.audio != null) {
            // A document that adopted the running call.
            emit(c, "audio", "ok");
            return;
        }
        c.audioWanted = true;
        host.requestMic(granted -> {
            if (call != c || c.finished) return;
            if (!granted) {
                emit(c, "audio", "microphone");
                finish(c, false, "mic_denied");
                return;
            }
            c.micGranted = true;
            if (c.sessionActive) startEngine(c);
        });
    }

    void stopAudio(String id) {
        Call c = current(id);
        if (c == null) return;
        c.audioWanted = false;
        stopEngine(c);
    }

    void setMutedFromPage(String id, boolean muted) {
        Call c = current(id);
        if (c != null) applyMute(c, muted, false);
    }

    // The pairing this call belongs to is gone (unpaired, paired elsewhere or
    // claimed again as another device): its audio must not reach another.
    void bindingChanged() {
        Call c = call;
        if (c == null) return;
        if (c.origin.equals(host.pairedOrigin())) {
            String now = VoiceAuth.deviceId(host.credential(c.origin));
            if (now != null && now.equals(c.deviceId)) return;
        }
        c.pageClosed = true;
        finish(c, false, "unpaired");
    }

    // ---- Internals

    // The device a call authenticates as, from its own header
    // ("Moa-Device <id>.<secret>" or "Moa-Voice <id>.<key>"), never from a
    // later read of the stored credential. Null when it names none.
    private static String deviceIdOf(String authorization) {
        if (authorization == null) return null;
        return VoiceAuth.deviceId(authorization.substring(authorization.indexOf(' ') + 1));
    }

    // The live call, if this id names it and the app is still paired with
    // the server that call belongs to.
    private Call current(String id) {
        Call c = call;
        if (c == null || c.finished || !c.id.equals(id) || !c.origin.equals(host.pairedOrigin())) return null;
        return c;
    }

    // Only while the app is still paired with the call's server.
    private void emit(Call c, String type, Object data) {
        if (c.origin.equals(host.pairedOrigin())) host.emit(c.id, type, data);
    }

    private void interrupt(Call c) {
        c.pending.clear();
        c.pendingBytes = 0;
        if (c.audio != null) c.audio.interrupt();
    }

    private void play(Call c, byte[] data) {
        if (c.audio != null) {
            c.audio.play(data);
            return;
        }
        // Audio that arrives before the speaker is on is kept, bounded.
        c.pending.add(data);
        c.pendingBytes += data.length;
        while (c.pendingBytes > CallProtocol.PENDING_MAX_BYTES && !c.pending.isEmpty()) {
            c.pendingBytes -= c.pending.remove(0).length;
        }
    }

    private void startEngine(Call c) {
        if (!c.audioWanted || !c.micGranted || c.audio != null || c.finished || c.hangingUp) return;
        Audio audio = null;
        try {
            host.systemCall().enableMicrophone();
            audio = host.createAudio(new Audios(c));
            if (c.muted) audio.setMuted(true);
            audio.start();
        } catch (Exception e) {
            if (audio != null) audio.stop();
            // Not the user's permission: the page may take the call over.
            host.emit(c.id, "audio", "audio_failed");
            String message = e.getMessage() == null ? "" : " " + CallProtocol.prefix(e.getMessage(), 40);
            finish(c, false, "engine " + e.getClass().getSimpleName() + message);
            return;
        }
        c.audio = audio;
        // Ready first: the page shows "speaking" only for playback it can see.
        emit(c, "audio", "ok");
        for (byte[] chunk : c.pending) audio.play(chunk);
        c.pending.clear();
        c.pendingBytes = 0;
    }

    private void stopEngine(Call c) {
        if (c.audio != null) c.audio.stop();
        c.audio = null;
    }

    private void applyMute(Call c, boolean muted, boolean fromSystem) {
        c.muted = muted;
        if (c.audio != null) c.audio.setMuted(muted);
        if (c.open) c.socket.sendText(CallProtocol.muteFrame(muted));
        if (fromSystem) emit(c, "mute", muted);
    }

    // Hung up from outside the app: silence now, and let the server end the
    // call so its record is saved; the page learns it from the server's
    // "ended".
    private void hangUp(Call c, String why) {
        if (c.hangingUp) return;
        c.audioWanted = false;
        stopEngine(c);
        c.hangingUp = true;
        if (c.open) {
            c.socket.sendText(CallProtocol.HANGUP_FRAME);
            c.hangupTimer = host.schedule(() -> {
                if (call == c) finish(c, true, why + "_timeout");
            }, CallProtocol.HANGUP_TIMEOUT_MILLIS);
        } else {
            finish(c, true, why + "_before_open");
        }
    }

    // Ends the call once: audio off, socket closed, system call ended and,
    // unless the page closed it itself, the page told. why travels as the
    // WebSocket close reason, which the server logs.
    private void finish(Call c, boolean remote, String why) {
        if (c.finished) return;
        c.finished = true;
        if (c.hangupTimer != null) c.hangupTimer.run();
        c.hangupTimer = null;
        stopEngine(c);
        String reason = CallProtocol.closeReason(why, host.micState());
        c.socket.close(1000, reason);
        if (c.micMissing) host.hangupCall(c.origin, c.callId, c.authorization);
        if (call == c) call = null;
        host.systemCall().end(remote && !(c.serverEnded || c.hangingUp));
        if (!c.pageClosed) emit(c, "close", reason);
    }

    // ---- Callbacks, one object per source so each knows its call

    private final class Sockets implements SocketEvents {
        private final Call c;

        Sockets(Call c) {
            this.c = c;
        }

        private boolean live() {
            return call == c && !c.finished;
        }

        @Override
        public void opened() {
            if (!live()) return;
            c.open = true;
            if (c.micMissing) {
                // Joined only to leave with a reason the server's journal keeps.
                finish(c, false, "mic_not_granted");
                return;
            }
            host.systemCall().connected();
            emit(c, "open", null);
        }

        @Override
        public void text(String text) {
            if (!live()) return;
            switch (CallProtocol.parseControl(text)) {
                case INTERRUPT:
                    interrupt(c);
                    break;
                case ENDED:
                    c.serverEnded = true;
                    break;
                default:
            }
            emit(c, "message", text);
        }

        @Override
        public void binary(byte[] data) {
            if (live()) play(c, data);
        }

        @Override
        public void closed(int code) {
            if (live()) finish(c, true, "server_closed " + code);
        }

        @Override
        public void failed(String code) {
            if (live()) finish(c, true, "socket " + code);
        }
    }

    private final class Audios implements AudioEvents {
        private final Call c;

        Audios(Call c) {
            this.c = c;
        }

        @Override
        public void frame(byte[] frame) {
            Socket socket = c.socket;
            if (c.finished || c.hangingUp || !c.open || c.muted || socket == null) return;
            if (CallProtocol.dropFrame(socket.queueSize(), frame.length)) return;
            socket.sendBinary(frame);
        }

        @Override
        public void playback(String type) {
            if (call == c && !c.finished) emit(c, "playback", type);
        }

        @Override
        public void failed() {
            if (call == c && !c.finished) finish(c, false, "route_rebuild");
        }
    }

    private final class SystemEvents implements SystemListener {
        private final Call c;

        SystemEvents(Call c) {
            this.c = c;
        }

        private boolean live() {
            return call == c && !c.finished;
        }

        @Override
        public void ready() {
            if (!live()) return;
            c.sessionActive = true;
            if (c.audioWanted) startEngine(c);
        }

        @Override
        public void failed(String code) {
            if (!live()) return;
            host.emit(c.id, "audio", "audio_failed");
            finish(c, false, "system " + code);
        }

        @Override
        public void hangUp() {
            if (live()) NativeCallManager.this.hangUp(c, "system_hangup");
        }

        @Override
        public void muted(boolean muted) {
            if (live() && c.muted != muted) applyMute(c, muted, true);
        }

        @Override
        public void routeChanged() {
            if (!live() || c.audio == null) return;
            try {
                c.audio.rebuild();
            } catch (Exception e) {
                finish(c, false, "route_rebuild");
            }
        }

        @Override
        public void interrupted() {
            if (live()) finish(c, false, "interrupted");
        }
    }
}

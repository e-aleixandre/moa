package com.ealeixandre.moa.app;

import android.media.AudioAttributes;
import android.media.AudioFormat;
import android.media.AudioRecord;
import android.media.AudioTrack;
import android.media.MediaRecorder;
import android.media.audiofx.AcousticEchoCanceler;
import android.media.audiofx.NoiseSuppressor;
import java.io.IOException;

// The call's microphone and speaker, the Android port of iOS NativeCallAudio:
// the voice-communication source (with echo cancellation and noise
// suppression where the device has them) cut into 20 ms s16le mono 24 kHz
// frames, and the provider's 24 kHz played back. There is no resampler:
// AudioRecord and AudioTrack take 24 kHz as it is, and a device that cannot
// open it fails start(), which hands the call back to the page's own audio.
//
// Playback is a non-blocking writer over PlaybackQueue, so a barge-in can
// flush the track at any moment without waiting for a blocked write. `lock`
// orders flush/rebuild against the writer; frames leave the capture thread
// without it.
final class CallAudio implements NativeCallManager.Audio {

    private static final int RATE = CallProtocol.SAMPLE_RATE;
    private static final long JOIN_MILLIS = 300;
    private static final long IDLE_MILLIS = 5;

    private final NativeCallManager.AudioEvents events;
    private final PlaybackQueue queue;
    private final PcmFramer framer = new PcmFramer(CallProtocol.FRAME_BYTES);
    private final Object lock = new Object();
    private AudioRecord record;
    private AudioTrack track;
    private AcousticEchoCanceler echo;
    private NoiseSuppressor noise;
    // Changes whenever the input is rebuilt, so a read that fails because its
    // AudioRecord was just released is not mistaken for a broken microphone.
    private int inputEpoch;
    private volatile boolean running;
    private volatile boolean muted;
    private Thread capture;
    private Thread writer;

    CallAudio(NativeCallManager.AudioEvents events) {
        this.events = events;
        this.queue = new PlaybackQueue(CallProtocol.MAX_QUEUED_BYTES, events::playback);
    }

    @Override
    public void start() throws IOException {
        synchronized (lock) {
            build();
            running = true;
        }
        capture = new Thread(this::captureLoop, "moa-call-capture");
        writer = new Thread(this::writeLoop, "moa-call-playback");
        capture.start();
        writer.start();
    }

    // Under lock. Throws with nothing left open.
    private void build() throws IOException {
        int minIn = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT);
        int minOut = AudioTrack.getMinBufferSize(RATE, AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT);
        if (minIn <= 0 || minOut <= 0) throw new IOException("24 kHz mono not supported");
        AudioRecord in = null;
        AudioTrack out = null;
        try {
            in = new AudioRecord(MediaRecorder.AudioSource.VOICE_COMMUNICATION, RATE,
                AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, Math.max(minIn, CallProtocol.FRAME_BYTES * 8));
            if (in.getState() != AudioRecord.STATE_INITIALIZED) throw new IOException("input not initialized");
            out = new AudioTrack.Builder()
                .setAudioAttributes(new AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
                    .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                    .build())
                .setAudioFormat(new AudioFormat.Builder()
                    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                    .setSampleRate(RATE)
                    .setChannelMask(AudioFormat.CHANNEL_OUT_MONO)
                    .build())
                .setBufferSizeInBytes(Math.max(minOut, RATE * 2 / 5))
                .setTransferMode(AudioTrack.MODE_STREAM)
                .build();
            if (out.getState() != AudioTrack.STATE_INITIALIZED) throw new IOException("output not initialized");
            if (AcousticEchoCanceler.isAvailable()) {
                echo = AcousticEchoCanceler.create(in.getAudioSessionId());
                if (echo != null) echo.setEnabled(true);
            }
            if (NoiseSuppressor.isAvailable()) {
                noise = NoiseSuppressor.create(in.getAudioSessionId());
                if (noise != null) noise.setEnabled(true);
            }
            in.startRecording();
            if (in.getRecordingState() != AudioRecord.RECORDSTATE_RECORDING) throw new IOException("input blocked");
            out.play();
        } catch (IOException | RuntimeException e) {
            release(in, out);
            throw e instanceof IOException ? (IOException) e : new IOException(e);
        }
        record = in;
        track = out;
        inputEpoch++;
    }

    // Under lock.
    private void releaseAll() {
        AudioRecord in = record;
        AudioTrack out = track;
        record = null;
        track = null;
        release(in, out);
    }

    private void release(AudioRecord in, AudioTrack out) {
        if (echo != null) echo.release();
        if (noise != null) noise.release();
        echo = null;
        noise = null;
        if (in != null) {
            try {
                in.stop();
            } catch (IllegalStateException ignored) {
                // Never started.
            }
            in.release();
        }
        if (out != null) {
            try {
                out.stop();
            } catch (IllegalStateException ignored) {
                // Never started.
            }
            out.release();
        }
    }

    private void captureLoop() {
        byte[] buffer = new byte[CallProtocol.FRAME_BYTES];
        while (running) {
            AudioRecord in;
            int epoch;
            synchronized (lock) {
                in = record;
                epoch = inputEpoch;
            }
            if (in == null) {
                sleep(IDLE_MILLIS);
                continue;
            }
            int n = in.read(buffer, 0, buffer.length);
            if (!running) return;
            if (n > 0) {
                // Muted: the audio is read and dropped, so no frame leaves.
                framer.push(buffer, 0, n, frame -> {
                    if (!muted) events.frame(frame);
                });
            } else if (n < 0) {
                synchronized (lock) {
                    if (epoch != inputEpoch || !running) continue;
                }
                running = false;
                events.failed();
                return;
            }
        }
    }

    private void writeLoop() {
        byte[] current = null;
        int offset = 0;
        int currentGeneration = 0;
        while (running) {
            boolean wrote = false;
            boolean failed = false;
            synchronized (lock) {
                AudioTrack out = track;
                if (out != null) {
                    if (current != null && currentGeneration != queue.generation()) current = null;
                    if (current == null) {
                        currentGeneration = queue.generation();
                        current = queue.poll();
                        offset = 0;
                    }
                    if (current != null) {
                        int n = out.write(current, offset, current.length - offset, AudioTrack.WRITE_NON_BLOCKING);
                        if (n < 0) {
                            failed = true;
                        } else {
                            offset += n;
                            wrote = n > 0;
                            if (offset >= current.length) current = null;
                        }
                    }
                    if (queue.busy()) {
                        queue.played(queue.generation(), (out.getPlaybackHeadPosition() & 0xFFFFFFFFL) * 2);
                    }
                }
            }
            if (failed) {
                running = false;
                events.failed();
                return;
            }
            if (!wrote) sleep(IDLE_MILLIS);
        }
    }

    private static void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    @Override
    public void play(byte[] pcm) {
        if (running) queue.offer(pcm);
    }

    // Barge-in. The track is flushed before the queue is cleared, so a stale
    // head position can only name the old generation.
    @Override
    public void interrupt() {
        synchronized (lock) {
            if (!running || track == null) return;
            track.pause();
            track.flush();
            queue.clear();
            track.play();
        }
    }

    @Override
    public void setMuted(boolean muted) {
        this.muted = muted;
    }

    @Override
    public void rebuild() throws IOException {
        synchronized (lock) {
            if (!running) return;
            releaseAll();
            build();
            queue.clear();
            framer.reset();
        }
    }

    @Override
    public void stop() {
        if (!running && capture == null) return;
        running = false;
        synchronized (lock) {
            releaseAll();
        }
        join(capture);
        join(writer);
        capture = null;
        writer = null;
    }

    private static void join(Thread thread) {
        if (thread == null || thread == Thread.currentThread()) return;
        try {
            thread.join(JOIN_MILLIS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}

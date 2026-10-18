package com.ealeixandre.moa.app;

import java.util.ArrayDeque;

// The audio waiting to be heard: bounded, and honest about when sound
// starts and ends, so the page can show "speaking". Pure bookkeeping; the
// AudioTrack writer and its playback-head poll sit on top (CallAudio).
//
// "Queued" is everything offered and not yet played, including what the
// track already holds. The generation changes on every clear, so progress
// measured before a barge-in cannot drain the reply that follows it.
final class PlaybackQueue {

    interface Listener {
        // "playing" | "drained"
        void playback(String type);
    }

    private final int maxBytes;
    private final Listener listener;
    private final ArrayDeque<byte[]> chunks = new ArrayDeque<>();
    private long offered;
    private long played;
    private int generation;

    PlaybackQueue(int maxBytes, Listener listener) {
        this.maxBytes = maxBytes;
        this.listener = listener;
    }

    // False when the audio was dropped: empty, or over the bound (the new
    // audio is dropped, as on the web).
    synchronized boolean offer(byte[] pcm) {
        int length = pcm.length & ~1;
        if (length == 0 || (offered - played) + length > maxBytes) return false;
        boolean start = offered == played;
        chunks.add(length == pcm.length ? pcm : java.util.Arrays.copyOf(pcm, length));
        offered += length;
        if (start) listener.playback("playing");
        return true;
    }

    // The next chunk for the writer, or null.
    synchronized byte[] poll() {
        return chunks.poll();
    }

    // Bytes played since the last clear, as measured from the track.
    synchronized void played(int forGeneration, long totalBytes) {
        if (forGeneration != generation || offered == played) return;
        played = Math.max(played, Math.min(totalBytes, offered));
        if (played == offered) listener.playback("drained");
    }

    // Barge-in or a rebuilt output: everything goes.
    synchronized void clear() {
        boolean wasBusy = offered != played;
        chunks.clear();
        offered = 0;
        played = 0;
        generation++;
        if (wasBusy) listener.playback("drained");
    }

    synchronized boolean busy() {
        return offered != played;
    }

    synchronized int generation() {
        return generation;
    }

    synchronized long queuedBytes() {
        return offered - played;
    }
}

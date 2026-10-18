package com.ealeixandre.moa.app;

import java.util.function.Consumer;

// Cuts whatever the microphone returns into whole frames. AudioRecord reads
// can be any length; the server wants 20 ms (960 B) each.
final class PcmFramer {

    private final int frameBytes;
    private final byte[] pending;
    private int filled;

    PcmFramer(int frameBytes) {
        this.frameBytes = frameBytes;
        this.pending = new byte[frameBytes];
    }

    void push(byte[] data, int offset, int length, Consumer<byte[]> out) {
        int end = offset + length;
        if (length <= 0 || offset < 0 || end > data.length) return;
        int at = offset;
        while (at < end) {
            int n = Math.min(frameBytes - filled, end - at);
            System.arraycopy(data, at, pending, filled, n);
            filled += n;
            at += n;
            if (filled == frameBytes) {
                out.accept(pending.clone());
                filled = 0;
            }
        }
    }

    int pendingBytes() {
        return filled;
    }

    void reset() {
        filled = 0;
    }
}

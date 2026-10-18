package com.ealeixandre.moa.app;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;

import java.util.ArrayList;
import java.util.List;
import org.junit.Test;

public class PcmFramerTest {

    private static byte[] ramp(int from, int n) {
        byte[] out = new byte[n];
        for (int i = 0; i < n; i++) out[i] = (byte) (from + i);
        return out;
    }

    private static List<byte[]> push(PcmFramer framer, byte[] data, int off, int len) {
        List<byte[]> out = new ArrayList<>();
        framer.push(data, off, len, out::add);
        return out;
    }

    @Test
    public void cutsExactFramesOfTwentyMilliseconds() {
        PcmFramer framer = new PcmFramer(CallProtocol.FRAME_BYTES);
        byte[] data = ramp(0, 960 * 3);
        List<byte[]> frames = push(framer, data, 0, data.length);
        assertEquals(3, frames.size());
        for (int i = 0; i < 3; i++) {
            assertEquals(960, frames.get(i).length);
            assertArrayEquals(java.util.Arrays.copyOfRange(data, i * 960, (i + 1) * 960), frames.get(i));
        }
    }

    @Test
    public void carriesTheRemainderToTheNextPush() {
        PcmFramer framer = new PcmFramer(960);
        byte[] data = ramp(0, 2000);
        List<byte[]> first = push(framer, data, 0, 1000);
        assertEquals(1, first.size());
        assertEquals(40, framer.pendingBytes());
        List<byte[]> second = push(framer, data, 1000, 1000);
        assertEquals(1, second.size());
        assertArrayEquals(java.util.Arrays.copyOfRange(data, 960, 1920), second.get(0));
        assertEquals(80, framer.pendingBytes());
    }

    @Test
    public void smallReadsAccumulateUntilAFrameIsFull() {
        PcmFramer framer = new PcmFramer(960);
        byte[] data = ramp(0, 960);
        int frames = 0;
        for (int i = 0; i < 960; i += 100) {
            frames += push(framer, data, i, Math.min(100, 960 - i)).size();
        }
        assertEquals(1, frames);
        assertEquals(0, framer.pendingBytes());
    }

    @Test
    public void emitsAFreshArrayPerFrame() {
        PcmFramer framer = new PcmFramer(4);
        List<byte[]> frames = push(framer, ramp(0, 8), 0, 8);
        frames.get(0)[0] = 99;
        assertEquals(4, frames.get(1)[0]);
    }

    @Test
    public void resetDropsWhatWasPending() {
        PcmFramer framer = new PcmFramer(960);
        push(framer, ramp(0, 500), 0, 500);
        framer.reset();
        assertEquals(0, framer.pendingBytes());
        assertEquals(0, push(framer, ramp(0, 500), 0, 500).size());
    }

    @Test
    public void ignoresEmptyAndNegativeReads() {
        PcmFramer framer = new PcmFramer(960);
        assertEquals(0, push(framer, new byte[0], 0, 0).size());
        assertEquals(0, push(framer, new byte[10], 0, -1).size());
        assertEquals(0, framer.pendingBytes());
    }
}

package com.ealeixandre.moa.app;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.List;
import org.junit.Test;

public class PlaybackQueueTest {

    private final List<String> events = new ArrayList<>();
    private final PlaybackQueue queue = new PlaybackQueue(CallProtocol.MAX_QUEUED_BYTES, events::add);

    private static byte[] pcm(int n) {
        return new byte[n];
    }

    @Test
    public void firstChunkStartsPlayingAndNothingRepeats() {
        assertTrue(queue.offer(pcm(960)));
        assertTrue(queue.offer(pcm(960)));
        assertEquals(List.of("playing"), events);
        assertTrue(queue.busy());
    }

    @Test
    public void drainsWhenEverythingOfferedHasPlayed() {
        queue.offer(pcm(960));
        queue.offer(pcm(960));
        queue.played(queue.generation(), 960);
        assertEquals(List.of("playing"), events);
        queue.played(queue.generation(), 1920);
        assertEquals(List.of("playing", "drained"), events);
        assertFalse(queue.busy());
        queue.played(queue.generation(), 1920);
        assertEquals(2, events.size());
    }

    @Test
    public void startsPlayingAgainAfterADrain() {
        queue.offer(pcm(960));
        queue.played(queue.generation(), 960);
        queue.offer(pcm(960));
        assertEquals(List.of("playing", "drained", "playing"), events);
    }

    @Test
    public void handsChunksToTheWriterInOrder() {
        byte[] a = {1, 2};
        byte[] b = {3, 4};
        queue.offer(a);
        queue.offer(b);
        assertArrayEquals(a, queue.poll());
        assertArrayEquals(b, queue.poll());
        assertNull(queue.poll());
    }

    @Test
    public void dropsNewAudioOverSixtySeconds() {
        int chunk = 24000 * 2;
        for (int i = 0; i < 60; i++) assertTrue(queue.offer(pcm(chunk)));
        assertFalse(queue.offer(pcm(2)));
        assertEquals(CallProtocol.MAX_QUEUED_BYTES, queue.queuedBytes());
        // Playing frees room.
        queue.played(queue.generation(), chunk);
        assertTrue(queue.offer(pcm(chunk)));
    }

    @Test
    public void ignoresEmptyAudioAndATrailingOddByte() {
        assertFalse(queue.offer(pcm(0)));
        assertFalse(queue.offer(pcm(1)));
        assertTrue(events.isEmpty());
        assertTrue(queue.offer(pcm(5)));
        assertEquals(4, queue.poll().length);
    }

    @Test
    public void bargeInEmptiesTheQueueAndDrains() {
        queue.offer(pcm(960));
        queue.offer(pcm(960));
        int before = queue.generation();
        queue.clear();
        assertEquals(List.of("playing", "drained"), events);
        assertNull(queue.poll());
        assertEquals(0, queue.queuedBytes());
        assertFalse(queue.busy());
        assertEquals(before + 1, queue.generation());
    }

    @Test
    public void bargeInWhenIdleSaysNothing() {
        queue.clear();
        assertTrue(events.isEmpty());
    }

    @Test
    public void staleProgressFromBeforeABargeInIsIgnored() {
        queue.offer(pcm(960));
        int stale = queue.generation();
        queue.clear();
        queue.offer(pcm(960));
        queue.played(stale, 960);
        // The new reply is still queued, not drained by the old head position.
        assertTrue(queue.busy());
        assertEquals(List.of("playing", "drained", "playing"), events);
        queue.played(queue.generation(), 960);
        assertEquals("drained", events.get(events.size() - 1));
    }
}

package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.nio.charset.StandardCharsets;
import org.junit.Test;

public class CallProtocolTest {

    private static int bytes(String s) {
        return s.getBytes(StandardCharsets.UTF_8).length;
    }

    private static String repeat(String s, int n) {
        StringBuilder out = new StringBuilder();
        for (int i = 0; i < n; i++) out.append(s);
        return out.toString();
    }

    @Test
    public void constantsMatchTheWireContract() {
        // 20 ms of 24 kHz s16le mono.
        assertEquals(960, CallProtocol.FRAME_BYTES);
        assertEquals(24000, CallProtocol.SAMPLE_RATE);
        // One second of captured audio in flight, 60 s queued to play, 4 s held before the speaker.
        assertEquals(48000, CallProtocol.MAX_SENDING_BYTES);
        assertEquals(24000 * 2 * 60, CallProtocol.MAX_QUEUED_BYTES);
        assertEquals(24000 * 2 * 4, CallProtocol.PENDING_MAX_BYTES);
        assertEquals(5000, CallProtocol.HANGUP_TIMEOUT_MILLIS);
        assertEquals(123, CallProtocol.MAX_REASON_BYTES);
    }

    @Test
    public void shortReasonsPassUntouched() {
        assertEquals("native:page_closed mic=granted", CallProtocol.closeReason("page_closed", "granted"));
    }

    @Test
    public void emptyReasonIsUnspecified() {
        assertEquals("native:unspecified mic=denied", CallProtocol.closeReason("", "denied"));
        assertEquals("native:unspecified mic=undetermined", CallProtocol.closeReason(null, "undetermined"));
    }

    @Test
    public void truncatesToExactlyTheByteLimit() {
        String reason = CallProtocol.closeReason(repeat("a", 500), "granted");
        assertEquals(123, bytes(reason));
        assertTrue(reason.startsWith("native:aaaa"));
        assertEquals(123, bytes(CallProtocol.closeReason(repeat("a", 123))));
        assertEquals(123, bytes(CallProtocol.closeReason(repeat("a", 124))));
        assertEquals(122, bytes(CallProtocol.closeReason(repeat("a", 122))));
    }

    @Test
    public void neverCutsInsideACharacter() {
        // 2-, 3- and 4-byte characters landing across the 123-byte edge.
        for (String ch : new String[] {"é", "€", "\uD83D\uDE00"}) {
            for (int pad = 0; pad < 4; pad++) {
                String text = repeat("a", pad) + repeat(ch, 100);
                String out = CallProtocol.closeReason(text);
                assertTrue(bytes(out) <= 123);
                assertTrue(text.startsWith(out));
                // Decoding what was kept must round-trip: no half characters.
                assertEquals(out, new String(out.getBytes(StandardCharsets.UTF_8), StandardCharsets.UTF_8));
                assertFalse(out.contains("\uFFFD"));
                // And it is the longest such prefix.
                int next = text.offsetByCodePoints(out.length(), 1);
                assertTrue(bytes(text.substring(0, next)) > 123);
            }
        }
    }

    @Test
    public void controlFramesAreTheServersJson() {
        assertEquals("{\"type\":\"mute\",\"muted\":true}", CallProtocol.muteFrame(true));
        assertEquals("{\"type\":\"mute\",\"muted\":false}", CallProtocol.muteFrame(false));
        assertEquals("{\"type\":\"hangup\"}", CallProtocol.HANGUP_FRAME);
    }

    @Test
    public void parsesWhatTheNativeSideActsOn() {
        assertEquals(CallProtocol.Control.INTERRUPT, CallProtocol.parseControl("{\"type\":\"speech_started\"}"));
        assertEquals(CallProtocol.Control.INTERRUPT, CallProtocol.parseControl("{\"type\":\"state\",\"state\":\"listening\",\"interrupt\":true}"));
        assertEquals(CallProtocol.Control.ENDED, CallProtocol.parseControl("{\"type\":\"ended\",\"reason\":\"hangup\"}"));
    }

    @Test
    public void ignoresTheRest() {
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("{\"type\":\"state\",\"state\":\"speaking\"}"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("{\"type\":\"state\",\"interrupt\":false}"));
        // Only a real boolean true interrupts, as iOS (as? Bool == true).
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("{\"type\":\"state\",\"interrupt\":\"true\"}"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("{\"type\":\"state\",\"interrupt\":1}"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("{\"type\":\"pong\"}"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("{\"type\":\"needs_tap\",\"item_id\":\"x\"}"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("{\"type\":7}"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("{}"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("not json"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl("[]"));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl(""));
        assertEquals(CallProtocol.Control.NONE, CallProtocol.parseControl(null));
    }

    @Test
    public void backpressureDropsFreshFramesWhileASecondIsInFlight() {
        assertFalse(CallProtocol.dropFrame(0, 960));
        assertFalse(CallProtocol.dropFrame(47040, 960));
        assertTrue(CallProtocol.dropFrame(47041, 960));
        assertTrue(CallProtocol.dropFrame(48000, 960));
        assertTrue(CallProtocol.dropFrame(1_000_000, 960));
    }

    @Test
    public void prefixCountsCharactersNotBytes() {
        assertEquals("abc", CallProtocol.prefix("abcdef", 3));
        assertEquals("abc", CallProtocol.prefix("abc", 80));
        assertEquals("", CallProtocol.prefix(null, 80));
        String emoji = repeat("\uD83D\uDE00", 100);
        assertEquals(repeat("\uD83D\uDE00", 80), CallProtocol.prefix(emoji, 80));
    }
}

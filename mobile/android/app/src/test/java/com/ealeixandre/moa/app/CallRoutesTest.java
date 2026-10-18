package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;

import org.junit.Test;

// Where a call's sound starts: the loudspeaker, as the web and iOS
// (defaultToSpeaker), unless the user has a headset on.
public class CallRoutesTest {

    private static final int E = CallRoutes.EARPIECE;
    private static final int B = CallRoutes.BLUETOOTH;
    private static final int W = CallRoutes.WIRED_HEADSET;
    private static final int S = CallRoutes.SPEAKER;

    @Test
    public void usesTheLoudspeakerWhenNothingElseIsConnected() {
        assertEquals(S, CallRoutes.initial(E, E | S));
    }

    @Test
    public void staysOnAHeadsetTheSystemAlreadyChose() {
        assertEquals(0, CallRoutes.initial(B, E | B | S));
        assertEquals(0, CallRoutes.initial(W, E | W | S));
        assertEquals(0, CallRoutes.initial(S, E | S));
    }

    @Test
    public void movesToAHeadsetThatIsConnectedButNotInUse() {
        assertEquals(W, CallRoutes.initial(E, E | W | S));
        assertEquals(B, CallRoutes.initial(S, E | B | S));
        // Wired wins over Bluetooth, like the system's own order.
        assertEquals(W, CallRoutes.initial(E, E | W | B | S));
    }

    @Test
    public void leavesTheRouteAloneWhenTheLoudspeakerIsNotAnOption() {
        assertEquals(0, CallRoutes.initial(E, E));
        assertEquals(0, CallRoutes.initial(0, 0));
    }

    @Test
    public void onlyARealChangeIsARouteChange() {
        assertEquals(false, CallRoutes.changed(S, S));
        assertEquals(true, CallRoutes.changed(S, B));
        assertEquals(false, CallRoutes.changed(0, S));
    }
}

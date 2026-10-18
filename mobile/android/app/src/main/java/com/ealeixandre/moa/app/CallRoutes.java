package com.ealeixandre.moa.app;

// Audio routes of a call, as the Telecom CallAudioState bit values (kept as
// plain ints so the policy runs on the JVM).
final class CallRoutes {

    static final int EARPIECE = 0x1;
    static final int BLUETOOTH = 0x2;
    static final int WIRED_HEADSET = 0x4;
    static final int SPEAKER = 0x8;

    private CallRoutes() {}

    // The route to ask for when a call starts, or 0 to leave the system's:
    // a connected headset, else the loudspeaker (iOS defaultToSpeaker).
    static int initial(int current, int supportedMask) {
        if (current == BLUETOOTH || current == WIRED_HEADSET) return 0;
        if ((supportedMask & WIRED_HEADSET) != 0) return WIRED_HEADSET;
        if ((supportedMask & BLUETOOTH) != 0) return BLUETOOTH;
        if ((supportedMask & SPEAKER) != 0 && current != SPEAKER) return SPEAKER;
        return 0;
    }

    // previous 0: the first report, which is not a change.
    static boolean changed(int previous, int current) {
        return previous != 0 && previous != current;
    }
}

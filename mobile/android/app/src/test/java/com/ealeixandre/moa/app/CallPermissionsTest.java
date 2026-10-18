package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import org.junit.Test;

// What the app asks once, on the first opening, so a locked phone can ring:
// the microphone, notifications and, on Android 14+, the full-screen screen.
public class CallPermissionsTest {

    private static List<CallPermissions.Step> missing(int sdk, boolean mic, boolean notifications, boolean fullScreen) {
        return CallPermissions.missing(sdk, mic, notifications, fullScreen);
    }

    @Test
    public void everythingGrantedAsksForNothing() {
        assertEquals(Collections.emptyList(), missing(36, true, true, true));
    }

    @Test
    public void asksInOrderForWhatIsMissing() {
        assertEquals(Arrays.asList(CallPermissions.Step.MICROPHONE, CallPermissions.Step.NOTIFICATIONS, CallPermissions.Step.FULL_SCREEN),
            missing(36, false, false, false));
        assertEquals(Arrays.asList(CallPermissions.Step.FULL_SCREEN), missing(34, true, true, false));
        assertEquals(Arrays.asList(CallPermissions.Step.MICROPHONE), missing(36, false, true, true));
    }

    @Test
    public void olderAndroidHasNoRuntimeNotificationOrFullScreenSwitch() {
        assertEquals(Arrays.asList(CallPermissions.Step.MICROPHONE), missing(32, false, false, false));
        assertEquals(Arrays.asList(CallPermissions.Step.MICROPHONE, CallPermissions.Step.NOTIFICATIONS), missing(33, false, false, false));
        assertEquals(Collections.emptyList(), missing(26, true, false, false));
    }

    @Test
    public void asksOnlyOncePairedAndNeverAgainOnceAsked() {
        List<CallPermissions.Step> some = Arrays.asList(CallPermissions.Step.MICROPHONE);
        assertTrue(CallPermissions.shouldAsk(true, false, some));
        assertFalse("not paired yet", CallPermissions.shouldAsk(false, false, some));
        assertFalse("already asked", CallPermissions.shouldAsk(true, true, some));
        assertFalse("nothing missing", CallPermissions.shouldAsk(true, false, Collections.emptyList()));
    }
}

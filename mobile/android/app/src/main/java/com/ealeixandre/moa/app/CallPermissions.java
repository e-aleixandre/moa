package com.ealeixandre.moa.app;

import java.util.ArrayList;
import java.util.List;

// What the app asks for once, when it is opened, so a locked phone can ring
// and be answered: Android cannot ask while the screen is locked.
final class CallPermissions {

    enum Step {
        MICROPHONE,
        NOTIFICATIONS,
        // Android 14+: showing the call over the lock screen is a setting.
        FULL_SCREEN
    }

    private static final int TIRAMISU = 33;
    private static final int UPSIDE_DOWN_CAKE = 34;

    private CallPermissions() {}

    static List<Step> missing(int sdk, boolean microphone, boolean notifications, boolean fullScreen) {
        List<Step> steps = new ArrayList<>();
        if (!microphone) steps.add(Step.MICROPHONE);
        // Below 33 notifications are an app-level switch, not a prompt.
        if (sdk >= TIRAMISU && !notifications) steps.add(Step.NOTIFICATIONS);
        if (sdk >= UPSIDE_DOWN_CAKE && !fullScreen) steps.add(Step.FULL_SCREEN);
        return steps;
    }

    // Only for a paired phone (nothing can ring it before), once.
    static boolean shouldAsk(boolean paired, boolean alreadyAsked, List<Step> missing) {
        return paired && !alreadyAsked && !missing.isEmpty();
    }
}

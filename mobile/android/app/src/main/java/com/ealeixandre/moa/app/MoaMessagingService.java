package com.ealeixandre.moa.app;

import androidx.annotation.NonNull;
import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;
import java.util.Map;

// Receives the relay's data-only FCM messages (no `notification` block, so
// Google never sees a title or body and the app always does the drawing).
// On Android there is no separate extension process: this service is the
// counterpart of both the iOS Notification Service Extension (envelopes) and
// AppDelegate's challenge handling.
public class MoaMessagingService extends FirebaseMessagingService {

    @Override
    public void onMessageReceived(@NonNull RemoteMessage message) {
        Map<String, String> data = message.getData();
        switch (PushMessages.classify(data)) {
            case CHALLENGE:
                NativePushRegistration.get(this).handleChallenge(data.get("r"));
                return;
            case ENVELOPE:
                PushNotifier.show(this, data);
                return;
            default:
                // Not from the relay's closed schema: nothing to show.
        }
    }

    @Override
    public void onNewToken(@NonNull String token) {
        NativePushRegistration.get(this).refreshIfNeeded(true);
    }
}

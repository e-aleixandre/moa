package com.ealeixandre.moa.app;

import androidx.annotation.NonNull;
import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;
import java.util.Map;

// Receives the relay's data-only FCM messages (no `notification` block, so
// Google never sees a title or body and the app always does the drawing).
// On Android there is no separate extension process: this service is the
// counterpart of the iOS Notification Service Extension (envelopes), of
// AppDelegate's challenge handling and of PushKit's incoming-call push.
public class MoaMessagingService extends FirebaseMessagingService {

    @Override
    public void onMessageReceived(@NonNull RemoteMessage message) {
        Map<String, String> data = message.getData();
        switch (PushMessages.classify(data)) {
            case CHALLENGE:
                // Alerts and calls share the token and the challenge channel:
                // each answers only a challenge it is waiting for.
                NativePushRegistration.get(this).handleChallenge(data.get("r"));
                AndroidVoip.get(this).handleChallenge(data.get("r"));
                return;
            case ENVELOPE:
                // The key id in the envelope says whether it is a call.
                if (AndroidVoip.get(this).receive(data)) return;
                PushNotifier.show(this, data);
                return;
            default:
                // Not from the relay's closed schema: nothing to show.
        }
    }

    @Override
    public void onNewToken(@NonNull String token) {
        NativePushRegistration.get(this).refreshIfNeeded(true);
        AndroidVoip.get(this).refresh(true);
    }
}

package com.ealeixandre.moa.app;

import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import androidx.core.app.NotificationCompat;
import androidx.core.app.NotificationManagerCompat;
import java.util.Map;

// Shows one envelope: the Android side of iOS NotificationService.swift.
// The content is built from scratch from the authenticated plaintext; nothing
// the relay put next to the envelope reaches the notification except the
// opaque collapse id (as the replacement tag) and the envelope itself (in the
// tap intent, so a tap can re-authenticate it).
final class PushNotifier {

    private static final int NOTIFICATION_ID = 1;

    private PushNotifier() {}

    // Channel names are user-visible in system settings: Spanish UI with an
    // English fallback, from resources like the iOS Localizable.strings.
    static void ensureChannels(Context context) {
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        if (manager == null) return;
        manager.createNotificationChannel(channel(context, PushMessages.CHANNEL_URGENT, R.string.channel_urgent, true));
        manager.createNotificationChannel(channel(context, PushMessages.CHANNEL_ACTIVE, R.string.channel_active, true));
        manager.createNotificationChannel(channel(context, PushMessages.CHANNEL_PASSIVE, R.string.channel_passive, false));
    }

    private static NotificationChannel channel(Context context, String id, int name, boolean sound) {
        NotificationChannel channel = new NotificationChannel(id, context.getString(name), PushMessages.importanceFor(id));
        if (!sound) {
            channel.setSound(null, null);
            channel.enableVibration(false);
        }
        return channel;
    }

    static void show(Context context, Map<String, String> data) {
        ensureChannels(context);
        PushEnvelope.Keys keys = NativePushRegistration.get(context).currentKeys();
        PushEnvelope.Content content = PushEnvelope.open(data, keys, System.currentTimeMillis() / 1000);
        PushNavigator navigator = PushNavigator.get();
        if (PushMessages.suppress(content, navigator.visibleSession(), navigator.isForeground())) return;

        String channel;
        String title;
        String body;
        String group = null;
        if (content == null) {
            // Whatever cannot be authenticated is the localized fallback,
            // which sounds: losing a question is worse than an extra alert.
            channel = PushMessages.fallbackChannel();
            title = context.getString(R.string.push_fallback_title);
            body = context.getString(R.string.push_fallback_body);
        } else {
            channel = PushMessages.channelFor(content.level);
            title = content.title;
            body = content.body;
            group = content.thread;
        }
        String tag = PushMessages.tagFor(data, content);

        NotificationCompat.Builder builder = new NotificationCompat.Builder(context, channel)
            .setSmallIcon(R.drawable.ic_stat_moa)
            .setContentTitle(title)
            .setContentText(body)
            .setStyle(new NotificationCompat.BigTextStyle().bigText(body))
            .setAutoCancel(true)
            .setContentIntent(tapIntent(context, data, tag));
        if (group != null && !group.isEmpty()) builder.setGroup(group);

        NotificationManagerCompat manager = NotificationManagerCompat.from(context);
        if (!manager.areNotificationsEnabled()) return;
        try {
            manager.notify(tag, NOTIFICATION_ID, builder.build());
            if (group != null && !group.isEmpty()) {
                // Android bundles a group only under a summary. The summary
                // never alerts on its own; the child already did.
                manager.notify("group:" + group, NOTIFICATION_ID, new NotificationCompat.Builder(context, channel)
                    .setSmallIcon(R.drawable.ic_stat_moa)
                    .setGroup(group)
                    .setGroupSummary(true)
                    .setGroupAlertBehavior(NotificationCompat.GROUP_ALERT_CHILDREN)
                    .setAutoCancel(true)
                    .build());
            }
        } catch (SecurityException ignored) {
            // POST_NOTIFICATIONS was revoked between the check and the post.
        }
    }

    private static PendingIntent tapIntent(Context context, Map<String, String> data, String tag) {
        Intent intent = new Intent(context, MainActivity.class)
            .setAction("com.ealeixandre.moa.app.OPEN_PUSH")
            .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP | Intent.FLAG_ACTIVITY_CLEAR_TOP)
            .putExtra(PushNavigator.EXTRA_V, data.get("v"))
            .putExtra(PushNavigator.EXTRA_K, data.get("k"))
            .putExtra(PushNavigator.EXTRA_C, data.get("c"));
        return PendingIntent.getActivity(context, tag.hashCode(), intent,
            PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
    }
}

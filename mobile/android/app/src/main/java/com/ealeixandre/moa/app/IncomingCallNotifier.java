package com.ealeixandre.moa.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.media.AudioAttributes;
import android.media.RingtoneManager;
import android.net.Uri;
import androidx.core.app.NotificationCompat;
import androidx.core.app.NotificationManagerCompat;
import androidx.core.app.Person;

// What the phone shows while an invitation rings: the system's incoming-call
// notification (Answer and Decline, the ringtone repeating until it is gone)
// and, over the lock screen, the call screen. The app draws them itself:
// Telecom is self-managed and shows nothing.
final class IncomingCallNotifier {

    static final String CHANNEL = "moa_incoming_call";
    static final String ACTION_ANSWER = "com.ealeixandre.moa.app.CALL_ANSWER";
    static final String ACTION_DECLINE = "com.ealeixandre.moa.app.CALL_DECLINE";
    static final String EXTRA_INVITE = "invite";
    static final String EXTRA_NAME = "name";
    private static final int RINGING_ID = 3;
    private static final int MISSED_ID = 4;
    private static final long SLACK_MILLIS = 2000;

    private IncomingCallNotifier() {}

    static void ensureChannel(Context context) {
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        if (manager == null) return;
        NotificationChannel channel = new NotificationChannel(CHANNEL, context.getString(R.string.channel_incoming_call),
            NotificationManager.IMPORTANCE_HIGH);
        Uri ringtone = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE);
        channel.setSound(ringtone, new AudioAttributes.Builder()
            .setUsage(AudioAttributes.USAGE_NOTIFICATION_RINGTONE)
            .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
            .build());
        channel.enableVibration(true);
        channel.setVibrationPattern(new long[] { 0, 800, 600, 800, 600 });
        // The caller's name shows on the lock screen, as a phone call's does.
        channel.setLockscreenVisibility(Notification.VISIBILITY_PUBLIC);
        manager.createNotificationChannel(channel);
    }

    static void show(Context context, IncomingCalls.Ringing ringing) {
        ensureChannel(context);
        NotificationManagerCompat manager = NotificationManagerCompat.from(context);
        if (!manager.areNotificationsEnabled()) return;
        Person caller = new Person.Builder().setName(ringing.name).setImportant(true).build();
        PendingIntent screen = PendingIntent.getActivity(context, 0, IncomingCallActivity.intent(context, ringing),
            PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        long timeout = Math.max(1000, ringing.expiresAtMillis + SLACK_MILLIS - System.currentTimeMillis());
        Notification notification = new NotificationCompat.Builder(context, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_moa)
            .setContentText(context.getString(R.string.call_incoming))
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setOngoing(true)
            .setAutoCancel(false)
            .setContentIntent(screen)
            .setFullScreenIntent(screen, true)
            // A frozen process cannot cancel it: the system does, at expiry.
            .setTimeoutAfter(timeout)
            .setStyle(NotificationCompat.CallStyle.forIncomingCall(caller,
                action(context, ACTION_DECLINE, ringing.invite, 1), action(context, ACTION_ANSWER, ringing.invite, 2)))
            .build();
        // The ringtone repeats until the notification goes.
        notification.flags |= Notification.FLAG_INSISTENT;
        try {
            manager.notify(ringing.invite, RINGING_ID, notification);
        } catch (SecurityException ignored) {
            // POST_NOTIFICATIONS was revoked between the check and the post.
        }
    }

    static void cancel(Context context, String invite) {
        NotificationManagerCompat.from(context).cancel(invite, RINGING_ID);
    }

    static void missed(Context context, IncomingCalls.Ringing ringing) {
        // The app may never have shown an alert: the channel would not exist
        // and the system would drop the notification without a word.
        PushNotifier.ensureChannels(context);
        NotificationManagerCompat manager = NotificationManagerCompat.from(context);
        if (!manager.areNotificationsEnabled()) return;
        Intent open = new Intent(context, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP);
        Notification notification = new NotificationCompat.Builder(context, PushMessages.CHANNEL_ACTIVE)
            .setSmallIcon(R.drawable.ic_stat_moa)
            .setContentTitle(context.getString(R.string.call_missed))
            .setContentText(ringing.name)
            .setCategory(NotificationCompat.CATEGORY_MISSED_CALL)
            .setAutoCancel(true)
            .setContentIntent(PendingIntent.getActivity(context, 0, open, PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT))
            .build();
        try {
            manager.notify(ringing.invite, MISSED_ID, notification);
        } catch (SecurityException ignored) {
            // Revoked meanwhile.
        }
    }

    // Answer and Decline reach the call through a receiver: it runs with the
    // screen locked and never brings the app forward.
    private static PendingIntent action(Context context, String action, String invite, int code) {
        Intent intent = new Intent(context, CallActionReceiver.class).setAction(action).putExtra(EXTRA_INVITE, invite);
        return PendingIntent.getBroadcast(context, code, intent, PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
    }
}

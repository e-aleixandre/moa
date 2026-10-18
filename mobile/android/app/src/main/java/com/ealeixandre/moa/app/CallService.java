package com.ealeixandre.moa.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;
import androidx.core.app.NotificationCompat;
import androidx.core.app.Person;
import androidx.core.content.ContextCompat;

// What keeps a call alive with the screen locked or the app minimized: a
// foreground service of type phoneCall, upgraded to also hold the microphone
// when the audio starts (Android lets a foreground service open the
// microphone only if it declares the type). The ongoing-call notification is
// the system's call style, with Hang up. Without Telecom or this service the
// process would be frozen under the call.
//
// One service for one call. Hang up from the notification asks the call, not
// the socket: the server ends the call and keeps its record.
public class CallService extends Service {

    static final String CHANNEL = "moa_call";
    static final int NOTIFICATION_ID = 2;
    private static final String ACTION_START = "com.ealeixandre.moa.app.CALL_START";
    private static final String ACTION_HANGUP = "com.ealeixandre.moa.app.CALL_HANGUP";
    private static final String EXTRA_NAME = "name";
    private static final String EXTRA_TOKEN = "token";
    private static final long UP_WAIT_MILLIS = 2000;

    private static volatile CallService instance;
    // stop() arrived before the service had gone foreground; stopping it then
    // would make the system fault the app for a promise it did not keep.
    private static volatile boolean stopRequested;

    private Notification notification;

    // False when the system refused to start it (the app was not allowed to):
    // the call then runs only while the app is on screen.
    static boolean start(Context context, String name, int token) {
        stopRequested = false;
        try {
            ContextCompat.startForegroundService(context, new Intent(context, CallService.class)
                .setAction(ACTION_START).putExtra(EXTRA_NAME, name).putExtra(EXTRA_TOKEN, token));
            return true;
        } catch (RuntimeException e) {
            return false;
        }
    }

    static void stop(Context context) {
        if (instance == null) {
            stopRequested = true;
            return;
        }
        context.stopService(new Intent(context, CallService.class));
    }

    // Lets the running service hold the microphone. Throws when it is not
    // running as a foreground service or the system refuses the type.
    static void enableMicrophone() throws Exception {
        long deadline = System.currentTimeMillis() + UP_WAIT_MILLIS;
        CallService service;
        while ((service = instance) == null || service.notification == null) {
            if (System.currentTimeMillis() > deadline) throw new IllegalStateException("no call service");
            Thread.sleep(20);
        }
        service.upgrade();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        String action = intent == null ? null : intent.getAction();
        if (ACTION_HANGUP.equals(action)) {
            NativeCalls.requestHangUp(intent.getIntExtra(EXTRA_TOKEN, -1));
            return START_NOT_STICKY;
        }
        if (!ACTION_START.equals(action)) {
            stopSelf();
            return START_NOT_STICKY;
        }
        try {
            ensureChannel(this);
            Notification built = build(intent.getStringExtra(EXTRA_NAME), intent.getIntExtra(EXTRA_TOKEN, -1));
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                startForeground(NOTIFICATION_ID, built, ServiceInfo.FOREGROUND_SERVICE_TYPE_PHONE_CALL);
            } else {
                startForeground(NOTIFICATION_ID, built);
            }
            notification = built;
            instance = this;
            if (stopRequested) {
                stopRequested = false;
                stopSelf();
            }
        } catch (RuntimeException e) {
            stopSelf();
        }
        return START_NOT_STICKY;
    }

    private void upgrade() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            startForeground(NOTIFICATION_ID, notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_PHONE_CALL | ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE);
        }
    }

    private Notification build(String name, int token) {
        Person caller = new Person.Builder().setName(name == null || name.isEmpty() ? getString(R.string.app_name) : name).build();
        Intent hangUp = new Intent(this, CallService.class).setAction(ACTION_HANGUP).putExtra(EXTRA_TOKEN, token);
        PendingIntent hangUpIntent = PendingIntent.getService(this, token, hangUp, PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        Intent open = new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP);
        PendingIntent openIntent = PendingIntent.getActivity(this, 0, open, PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        return new NotificationCompat.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_moa)
            .setContentText(getString(R.string.call_ongoing))
            .setCategory(NotificationCompat.CATEGORY_CALL)
            // On a secure lock screen too, as a phone call's: the owner's name
            // and Hang Up, nothing of the conversation.
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setUsesChronometer(true)
            .setWhen(System.currentTimeMillis())
            .setContentIntent(openIntent)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setStyle(NotificationCompat.CallStyle.forOngoingCall(caller, hangUpIntent))
            .build();
    }

    static void ensureChannel(Context context) {
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        if (manager == null) return;
        NotificationChannel channel = new NotificationChannel(CHANNEL, context.getString(R.string.channel_call),
            NotificationManager.IMPORTANCE_LOW);
        channel.setSound(null, null);
        channel.enableVibration(false);
        channel.setLockscreenVisibility(Notification.VISIBILITY_PUBLIC);
        manager.createNotificationChannel(channel);
    }

    @Override
    public void onDestroy() {
        if (instance == this) instance = null;
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}

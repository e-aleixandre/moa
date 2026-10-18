package com.ealeixandre.moa.app;

import android.content.ComponentName;
import android.content.Context;
import android.media.AudioAttributes;
import android.media.AudioDeviceInfo;
import android.media.AudioFocusRequest;
import android.media.AudioManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.telecom.PhoneAccount;
import android.telecom.PhoneAccountHandle;
import android.telecom.TelecomManager;

// What the system knows of the call: the foreground service that keeps it
// alive and the Telecom registration that routes its audio. iOS's CallKit
// provider is the model (ready = didActivate).
//
// If Telecom refuses the call (no permission, no account, no answer in
// time) the call still runs, as iOS does without CallKit: the app sets the
// audio mode and the loudspeaker itself and takes audio focus, and loses the
// call when something else takes it. Telecom routes and this fallback never
// mix: startBluetoothSco is not used anywhere.
//
// Everything here runs on the call thread; Telecom's callbacks are posted to
// it by the Connection classes through the poster.
final class CallSystem implements NativeCallManager.SystemCall {

    interface Poster {
        void post(Runnable task);
    }

    private enum Mode {
        NONE,
        PENDING,
        TELECOM,
        FALLBACK
    }

    private static final String ACCOUNT_ID = "moa-call";
    private static final long TELECOM_WAIT_MILLIS = 4000;

    private final Context context;
    private final Poster poster;
    private final NativeCallManager.Host host;
    private final Handler main = new Handler(Looper.getMainLooper());
    private NativeCallManager.SystemListener listener;
    private Mode mode = Mode.NONE;
    private CallConnection connection;
    private Runnable telecomTimeout;
    private AudioFocusRequest focus;
    private String name = "";
    private int lastRoute;
    private boolean lastMuted;
    private boolean serviceUp;

    CallSystem(Context context, Poster poster, NativeCallManager.Host host) {
        this.context = context.getApplicationContext();
        this.poster = poster;
        this.host = host;
    }

    @Override
    public void start(String name, NativeCallManager.SystemListener listener) {
        this.listener = listener;
        this.name = name;
        this.lastRoute = 0;
        this.lastMuted = false;
        serviceUp = CallService.start(context, name);
        TelecomManager telecom = context.getSystemService(TelecomManager.class);
        if (telecom == null) {
            useFallback();
            return;
        }
        PhoneAccountHandle handle = handle();
        mode = Mode.PENDING;
        try {
            telecom.registerPhoneAccount(PhoneAccount.builder(handle, context.getString(R.string.app_name))
                .setCapabilities(PhoneAccount.CAPABILITY_SELF_MANAGED)
                .addSupportedUriScheme(PhoneAccount.SCHEME_SIP)
                .build());
            telecomTimeout = host.schedule(() -> telecomFailedOnCallThread("timeout"), TELECOM_WAIT_MILLIS);
            Bundle extras = new Bundle();
            extras.putParcelable(TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE, handle);
            telecom.placeCall(Uri.fromParts(PhoneAccount.SCHEME_SIP, "moa", null), extras);
        } catch (RuntimeException e) {
            telecomFailedOnCallThread("refused");
        }
    }

    @Override
    public void connected() {
        if (connection != null) connection.setActive();
    }

    @Override
    public void enableMicrophone() throws Exception {
        if (!serviceUp) throw new IllegalStateException("no call service");
        CallService.enableMicrophone();
    }

    @Override
    public void end(boolean failed) {
        if (mode == Mode.NONE) return;
        if (telecomTimeout != null) telecomTimeout.run();
        telecomTimeout = null;
        if (connection != null) connection.end(failed);
        connection = null;
        if (mode == Mode.FALLBACK) restoreAudioMode();
        abandonFocus();
        CallService.stop(context);
        mode = Mode.NONE;
        listener = null;
    }

    // ---- From Telecom (any thread)

    void connectionCreated(CallConnection created) {
        poster.post(() -> {
            if (mode != Mode.PENDING) {
                // Late, or the call is already over.
                created.end(false);
                return;
            }
            if (telecomTimeout != null) telecomTimeout.run();
            telecomTimeout = null;
            connection = created;
            mode = Mode.TELECOM;
            created.describe(name);
            created.setDialing();
            requestFocus();
            if (listener != null) listener.ready();
        });
    }

    void telecomFailed(String code) {
        poster.post(() -> telecomFailedOnCallThread(code));
    }

    void hangUpRequested(CallConnection from) {
        poster.post(() -> {
            if (from == connection && listener != null) listener.hangUp();
        });
    }

    // The ongoing-call notification's Hang up.
    void requestHangUp() {
        poster.post(() -> {
            if (mode != Mode.NONE && listener != null) listener.hangUp();
        });
    }

    void audioState(CallConnection from, int route, int supportedMask, boolean muted) {
        poster.post(() -> {
            if (from != connection || listener == null) return;
            if (lastRoute == 0) {
                int want = CallRoutes.initial(route, supportedMask);
                if (want != 0) from.route(want);
            } else if (CallRoutes.changed(lastRoute, route)) {
                listener.routeChanged();
            }
            lastRoute = route;
            if (muted != lastMuted) {
                lastMuted = muted;
                listener.muted(muted);
            }
        });
    }

    // ---- Internals

    private void telecomFailedOnCallThread(String code) {
        if (mode != Mode.PENDING) return;
        if (telecomTimeout != null) telecomTimeout.run();
        telecomTimeout = null;
        useFallback();
    }

    private void useFallback() {
        mode = Mode.FALLBACK;
        AudioManager audio = context.getSystemService(AudioManager.class);
        if (audio != null) {
            audio.setMode(AudioManager.MODE_IN_COMMUNICATION);
            speaker(audio, true);
        }
        requestFocus();
        if (listener != null) listener.ready();
    }

    @SuppressWarnings("deprecation")
    private void speaker(AudioManager audio, boolean on) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            if (!on) {
                audio.clearCommunicationDevice();
                return;
            }
            for (AudioDeviceInfo device : audio.getAvailableCommunicationDevices()) {
                if (device.getType() == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER) {
                    audio.setCommunicationDevice(device);
                    return;
                }
            }
        } else {
            audio.setSpeakerphoneOn(on);
        }
    }

    private void restoreAudioMode() {
        AudioManager audio = context.getSystemService(AudioManager.class);
        if (audio == null) return;
        speaker(audio, false);
        audio.setMode(AudioManager.MODE_NORMAL);
    }

    private void requestFocus() {
        AudioManager audio = context.getSystemService(AudioManager.class);
        if (audio == null) return;
        focus = new AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT)
            .setAudioAttributes(new AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
                .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                .build())
            .setOnAudioFocusChangeListener(change -> {
                // With Telecom the system ends or holds the call itself; only
                // the fallback has nobody else to say it.
                if (change != AudioManager.AUDIOFOCUS_LOSS && change != AudioManager.AUDIOFOCUS_LOSS_TRANSIENT) return;
                poster.post(() -> {
                    if (mode == Mode.FALLBACK && listener != null) listener.interrupted();
                });
            }, main)
            .build();
        audio.requestAudioFocus(focus);
    }

    private void abandonFocus() {
        if (focus == null) return;
        AudioManager audio = context.getSystemService(AudioManager.class);
        if (audio != null) audio.abandonAudioFocusRequest(focus);
        focus = null;
    }

    private PhoneAccountHandle handle() {
        return new PhoneAccountHandle(new ComponentName(context, CallConnectionService.class), ACCOUNT_ID);
    }
}

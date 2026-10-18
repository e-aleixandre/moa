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

    // Answering or declining the ringing invitation from outside the app.
    interface RingingListener {
        void answer(String invite);

        void decline(String invite);
    }

    private enum Mode {
        NONE,
        PENDING,
        TELECOM,
        FALLBACK
    }

    static final String EXTRA_INVITE = "moa_invite";
    // Names the call a Telecom request was placed for, so an answer that
    // arrives after that call is over cannot be taken for the next one's.
    static final String EXTRA_CALL = "moa_call";
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
    // The current call's identity: moves on with every call, and travels in
    // the Telecom request and the service's notification actions. -1: none yet.
    private volatile int callToken = -1;
    // The socket opened before Telecom answered.
    private boolean socketOpen;
    private RingingListener ringingListener;
    private CallConnection ringingConnection;
    private String ringingInvite;
    private String ringingName = "";

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
        this.socketOpen = false;
        int token = ++callToken;
        serviceUp = CallService.start(context, name, token);
        TelecomManager telecom = context.getSystemService(TelecomManager.class);
        if (telecom == null) {
            useFallback();
            return;
        }
        PhoneAccountHandle handle = handle();
        mode = Mode.PENDING;
        try {
            registerAccount(telecom);
            telecomTimeout = host.schedule(() -> telecomFailedOnCallThread("timeout"), TELECOM_WAIT_MILLIS);
            Bundle extras = new Bundle();
            extras.putParcelable(TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE, handle);
            Bundle outgoing = new Bundle();
            outgoing.putInt(EXTRA_CALL, token);
            extras.putBundle(TelecomManager.EXTRA_OUTGOING_CALL_EXTRAS, outgoing);
            telecom.placeCall(Uri.fromParts(PhoneAccount.SCHEME_SIP, "moa", null), extras);
        } catch (RuntimeException e) {
            telecomFailedOnCallThread("refused");
        }
    }

    void setRingingListener(RingingListener listener) {
        ringingListener = listener;
    }

    // An invitation is ringing: Telecom is told so headsets, watches and the
    // car can answer it. If Telecom refuses, the notification rings alone.
    void ringing(String invite, String name) {
        ringingInvite = invite;
        ringingName = name;
        ringingConnection = null;
        TelecomManager telecom = context.getSystemService(TelecomManager.class);
        if (telecom == null) return;
        try {
            registerAccount(telecom);
            Bundle incoming = new Bundle();
            incoming.putString(EXTRA_INVITE, invite);
            Bundle extras = new Bundle();
            extras.putParcelable(TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE, handle());
            extras.putBundle(TelecomManager.EXTRA_INCOMING_CALL_EXTRAS, incoming);
            telecom.addNewIncomingCall(handle(), extras);
        } catch (RuntimeException e) {
            // Not registered with Telecom: the call still rings and answers.
        }
    }

    // The invitation stopped ringing without being joined. cause is a
    // DisconnectCause code.
    void ringingEnded(String invite, int cause) {
        if (!invite.equals(ringingInvite)) return;
        CallConnection connection = ringingConnection;
        ringingConnection = null;
        ringingInvite = null;
        if (connection != null) connection.endWith(cause);
    }

    @Override
    public void startIncoming(String name, NativeCallManager.SystemListener listener) {
        this.listener = listener;
        this.name = name;
        this.lastRoute = 0;
        this.lastMuted = false;
        this.socketOpen = false;
        serviceUp = CallService.start(context, name, ++callToken);
        CallConnection ringingNow = ringingConnection;
        ringingConnection = null;
        ringingInvite = null;
        if (ringingNow == null) {
            // Telecom never took the invitation: the app sets the audio up.
            useFallback();
            return;
        }
        connection = ringingNow;
        mode = Mode.TELECOM;
        ringingNow.describe(name);
        ringingNow.setActive();
        requestFocus();
        listener.ready();
    }

    @Override
    public void connected() {
        socketOpen = true;
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
            if (mode != Mode.PENDING || created.token() != callToken) {
                // Late, for a call that is already over, or the call is over.
                created.end(false);
                return;
            }
            if (telecomTimeout != null) telecomTimeout.run();
            telecomTimeout = null;
            connection = created;
            mode = Mode.TELECOM;
            created.describe(name);
            if (socketOpen) {
                created.setActive();
            } else {
                created.setDialing();
            }
            requestFocus();
            if (listener != null) listener.ready();
        });
    }

    void telecomFailed(String code, int token) {
        poster.post(() -> {
            if (token == callToken) telecomFailedOnCallThread(code);
        });
    }

    void incomingConnectionCreated(CallConnection created, String invite) {
        poster.post(() -> {
            if (!invite.equals(ringingInvite) || ringingConnection != null) {
                // Late, or the invitation is already over.
                created.end(false);
                return;
            }
            ringingConnection = created;
            created.describe(ringingName);
        });
    }

    void answerRequested(CallConnection from) {
        poster.post(() -> {
            if (from == ringingConnection && ringingListener != null) ringingListener.answer(ringingInvite);
        });
    }

    void rejectRequested(CallConnection from) {
        poster.post(() -> {
            if (from == ringingConnection && ringingListener != null) ringingListener.decline(ringingInvite);
        });
    }

    void hangUpRequested(CallConnection from) {
        poster.post(() -> {
            if (from == ringingConnection && ringingListener != null) {
                ringingListener.decline(ringingInvite);
            } else if (from == connection && listener != null) {
                listener.hangUp();
            }
        });
    }

    // The ongoing-call notification's Hang up, for the call whose
    // notification it was: one that outlived its call hangs up nothing.
    void requestHangUp(int token) {
        poster.post(() -> {
            if (token == callToken && mode != Mode.NONE && listener != null) listener.hangUp();
        });
    }

    void requestHangUp() {
        requestHangUp(callToken);
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
        // Audio the system did not grant is not audio the call may start on.
        if (!requestFocus()) {
            if (listener != null) listener.failed("audio_focus");
            return;
        }
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

    private boolean requestFocus() {
        AudioManager audio = context.getSystemService(AudioManager.class);
        if (audio == null) return true;
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
        return audio.requestAudioFocus(focus) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED;
    }

    private void abandonFocus() {
        if (focus == null) return;
        AudioManager audio = context.getSystemService(AudioManager.class);
        if (audio != null) audio.abandonAudioFocusRequest(focus);
        focus = null;
    }

    private void registerAccount(TelecomManager telecom) {
        telecom.registerPhoneAccount(PhoneAccount.builder(handle(), context.getString(R.string.app_name))
            .setCapabilities(PhoneAccount.CAPABILITY_SELF_MANAGED)
            .addSupportedUriScheme(PhoneAccount.SCHEME_SIP)
            .build());
    }

    private PhoneAccountHandle handle() {
        return new PhoneAccountHandle(new ComponentName(context, CallConnectionService.class), ACCOUNT_ID);
    }
}

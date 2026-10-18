package com.ealeixandre.moa.app;

import android.net.Uri;
import android.telecom.CallAudioState;
import android.telecom.Connection;
import android.telecom.DisconnectCause;
import android.telecom.PhoneAccount;
import android.telecom.TelecomManager;

// One call as Telecom sees it. It only reports what the system asks of the
// call (hang up, mute, a new audio route) to CallSystem; the call itself
// lives in NativeCallManager.
final class CallConnection extends Connection {

    private final CallSystem system;

    CallConnection(CallSystem system) {
        this.system = system;
        setConnectionProperties(PROPERTY_SELF_MANAGED);
        setConnectionCapabilities(CAPABILITY_MUTE);
        setAudioModeIsVoip(true);
        setAddress(Uri.fromParts(PhoneAccount.SCHEME_SIP, "moa", null), TelecomManager.PRESENTATION_ALLOWED);
        setInitializing();
    }

    void describe(String name) {
        setCallerDisplayName(name, TelecomManager.PRESENTATION_ALLOWED);
    }

    void end(boolean failed) {
        setDisconnected(new DisconnectCause(failed ? DisconnectCause.ERROR : DisconnectCause.LOCAL));
        destroy();
    }

    @Override
    public void onDisconnect() {
        system.hangUpRequested(this);
    }

    @Override
    public void onAbort() {
        system.hangUpRequested(this);
    }

    @SuppressWarnings("deprecation")
    @Override
    public void onCallAudioStateChanged(CallAudioState state) {
        if (state == null) return;
        system.audioState(this, state.getRoute(), state.getSupportedRouteMask(), state.isMuted());
    }

    @SuppressWarnings("deprecation")
    void route(int route) {
        setAudioRoute(route);
    }
}

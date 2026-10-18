package com.ealeixandre.moa.app;

import android.os.Bundle;
import android.telecom.Connection;
import android.telecom.ConnectionRequest;
import android.telecom.ConnectionService;
import android.telecom.DisconnectCause;
import android.telecom.PhoneAccountHandle;
import android.telecom.TelecomManager;

// The system's entry for the calls the app registers (self-managed: the app
// draws its own call screen and runs its own audio; Telecom adds routing to
// headsets, the car and watches, hanging up from them, and priority over
// other calls). It is the platform's ConnectionService rather than Jetpack
// Core-Telecom: that library is Kotlin-only and this build is Java.
public class CallConnectionService extends ConnectionService {

    @Override
    public Connection onCreateOutgoingConnection(PhoneAccountHandle account, ConnectionRequest request) {
        CallConnection connection = new CallConnection(NativeCalls.system(this), false);
        NativeCalls.system(this).connectionCreated(connection);
        return connection;
    }

    @Override
    public void onCreateOutgoingConnectionFailed(PhoneAccountHandle account, ConnectionRequest request) {
        NativeCalls.system(this).telecomFailed("create_failed");
    }

    @Override
    public Connection onCreateIncomingConnection(PhoneAccountHandle account, ConnectionRequest request) {
        Bundle extras = request.getExtras() == null ? null : request.getExtras().getBundle(TelecomManager.EXTRA_INCOMING_CALL_EXTRAS);
        String invite = extras == null ? null : extras.getString(CallSystem.EXTRA_INVITE);
        if (invite == null) return Connection.createFailedConnection(new DisconnectCause(DisconnectCause.REJECTED));
        // Answered and declined through the app's own notification and call
        // screen; Telecom only adds headsets, the car and watches.
        CallConnection connection = new CallConnection(NativeCalls.system(this), true);
        NativeCalls.system(this).incomingConnectionCreated(connection, invite);
        return connection;
    }
}

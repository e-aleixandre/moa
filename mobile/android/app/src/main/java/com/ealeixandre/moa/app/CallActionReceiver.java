package com.ealeixandre.moa.app;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

// Answer and Decline on the incoming-call notification.
public class CallActionReceiver extends BroadcastReceiver {

    @Override
    public void onReceive(Context context, Intent intent) {
        String invite = intent.getStringExtra(IncomingCallNotifier.EXTRA_INVITE);
        if (invite == null) return;
        if (IncomingCallNotifier.ACTION_ANSWER.equals(intent.getAction())) {
            NativeCalls.answer(context, invite);
        } else if (IncomingCallNotifier.ACTION_DECLINE.equals(intent.getAction())) {
            NativeCalls.decline(context, invite);
        }
    }
}

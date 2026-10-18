package com.ealeixandre.moa.app;

import android.content.Context;
import android.telecom.DisconnectCause;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;

// Every platform edge of the ringing invitation, for IncomingCalls: the
// notification and call screen, Telecom, the network and the call itself.
// Runs on the call thread, like the manager it hands the answered call to.
final class AndroidIncomingHost implements IncomingCalls.Host {

    private final Context context;
    private final ScheduledExecutorService thread;
    private final AndroidCallHost host;
    private final NativeCallManager manager;

    AndroidIncomingHost(Context context, AndroidCallHost host, NativeCallManager manager) {
        this.context = context.getApplicationContext();
        this.thread = host.thread();
        this.host = host;
        this.manager = manager;
    }

    @Override
    public long now() {
        return System.currentTimeMillis();
    }

    @Override
    public Runnable schedule(Runnable task, long delayMillis) {
        try {
            ScheduledFuture<?> future = thread.schedule(task, delayMillis, TimeUnit.MILLISECONDS);
            return () -> future.cancel(false);
        } catch (RejectedExecutionException e) {
            return () -> { };
        }
    }

    @Override
    public boolean callInProgress() {
        return manager.hasCall();
    }

    @Override
    public void ring(IncomingCalls.Ringing r) {
        IncomingCallNotifier.show(context, r);
        host.system().ringing(r.invite, r.name);
    }

    @Override
    public void answered(IncomingCalls.Ringing r) {
        IncomingCallNotifier.cancel(context, r.invite);
        IncomingCallActivity.dismiss(r.invite);
    }

    @Override
    public void ended(IncomingCalls.Ringing r, IncomingCalls.Ended why) {
        IncomingCallNotifier.cancel(context, r.invite);
        IncomingCallActivity.dismiss(r.invite);
        host.system().ringingEnded(r.invite, cause(why));
        if (why == IncomingCalls.Ended.UNANSWERED) IncomingCallNotifier.missed(context, r);
    }

    @Override
    public void poll(IncomingCalls.Ringing r, IncomingCalls.Reply reply) {
        host.invites().poll(r.origin, r.authorization, r.invite, reply);
    }

    @Override
    public void post(IncomingCalls.Ringing r, String path, IncomingCalls.Reply reply) {
        host.invites().post(r.origin, r.authorization, path, reply);
    }

    @Override
    public boolean startCall(IncomingCalls.Ringing r, String callId, String url) {
        return manager.openIncoming(r.name, r.sessionId, callId, r.origin, url, r.authorization);
    }

    private static int cause(IncomingCalls.Ended why) {
        switch (why) {
            case UNANSWERED:
                return DisconnectCause.MISSED;
            case ANSWERED_ELSEWHERE:
                return DisconnectCause.ANSWERED_ELSEWHERE;
            case DECLINED_HERE:
                return DisconnectCause.REJECTED;
            case FAILED:
                return DisconnectCause.ERROR;
            default:
                return DisconnectCause.REMOTE;
        }
    }
}

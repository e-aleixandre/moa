package com.ealeixandre.moa.app;

import static org.junit.Assert.*;
import static org.mockito.Mockito.*;
import static org.mockito.ArgumentMatchers.*;
import android.content.Context;
import android.media.AudioManager;
import android.os.Handler;
import android.os.Looper;
import android.telecom.TelecomManager;
import org.junit.Test;
import org.mockito.MockedStatic;
import org.mockito.MockedConstruction;

// Tests the real Android adapter, not a second implementation of its call state.
public class ReviewAndroidSystemTest {
    @Test public void aTelecomConnectionCreatedAfterSocketOpenMustStartActive() {
        try (MockedStatic<Looper> looper = mockStatic(Looper.class);
             MockedConstruction<Handler> handler = mockConstruction(Handler.class);
             MockedStatic<CallService> services = mockStatic(CallService.class);
             MockedStatic<android.telecom.PhoneAccount> accounts = mockStatic(android.telecom.PhoneAccount.class);
             MockedConstruction<android.telecom.PhoneAccountHandle> handles = mockConstruction(android.telecom.PhoneAccountHandle.class);
             MockedConstruction<android.content.ComponentName> names = mockConstruction(android.content.ComponentName.class);
             MockedConstruction<android.os.Bundle> bundles = mockConstruction(android.os.Bundle.class);
             MockedStatic<android.net.Uri> uris = mockStatic(android.net.Uri.class)) {
            Context context = mock(Context.class);
            when(context.getApplicationContext()).thenReturn(context);
            when(context.getSystemService(TelecomManager.class)).thenReturn(mock(TelecomManager.class));
            android.telecom.PhoneAccount.Builder builder = mock(android.telecom.PhoneAccount.Builder.class, RETURNS_SELF);
            accounts.when(() -> android.telecom.PhoneAccount.builder(any(), any())).thenReturn(builder);
            NativeCallManager.Host host = mock(NativeCallManager.Host.class);
            when(host.schedule(any(), anyLong())).thenReturn(() -> {});
            CallSystem system = new CallSystem(context, Runnable::run, host);
            system.start("Moa", mock(NativeCallManager.SystemListener.class));
            // The native socket's opened event is independent of the Telecom binder response.
            system.connected();
            CallConnection connection = mock(CallConnection.class);
            system.connectionCreated(connection);
            verify(connection).setActive();
        }
    }
    @Test public void aQueuedNotificationHangupCannotHangUpTheNextCall() {
        try (MockedStatic<Looper> looper = mockStatic(Looper.class);
             MockedConstruction<Handler> handler = mockConstruction(Handler.class);
             MockedStatic<CallService> services = mockStatic(CallService.class)) {
            Context context = mock(Context.class);
            when(context.getApplicationContext()).thenReturn(context);
            java.util.List<Runnable> tasks = new java.util.ArrayList<>();
            CallSystem system = new CallSystem(context, tasks::add, mock(NativeCallManager.Host.class));
            NativeCallManager.SystemListener old = mock(NativeCallManager.SystemListener.class);
            NativeCallManager.SystemListener next = mock(NativeCallManager.SystemListener.class);
            system.start("old", old);
            // Notification A is handled on main; its call-thread callback is not run yet.
            system.requestHangUp();
            system.end(false);
            system.start("new", next);
            tasks.remove(0).run();
            verify(next, never()).hangUp();
        }
    }
    @Test public void aTelecomConnectionForThePreviousCallCannotAttachToItsReplacement() {
        try (MockedStatic<Looper> looper = mockStatic(Looper.class);
             MockedConstruction<Handler> handler = mockConstruction(Handler.class);
             MockedStatic<CallService> services = mockStatic(CallService.class)) {
            Context context = mock(Context.class);
            when(context.getApplicationContext()).thenReturn(context);
            TelecomManager telecom = mock(TelecomManager.class);
            when(context.getSystemService(TelecomManager.class)).thenReturn(telecom);
            NativeCallManager.Host host = mock(NativeCallManager.Host.class);
            when(host.schedule(any(), anyLong())).thenReturn(() -> {});
            CallSystem system = new CallSystem(context, Runnable::run, host);
            NativeCallManager.SystemListener old = mock(NativeCallManager.SystemListener.class);
            NativeCallManager.SystemListener next = mock(NativeCallManager.SystemListener.class);
            // Android PhoneAccount builders are stubs on a plain JVM, so bypass only account registration
            // by mocking the builder construction/static factory; the real scheduling/connection adapter runs.
            try (MockedStatic<android.telecom.PhoneAccount> accounts = mockStatic(android.telecom.PhoneAccount.class);
                 MockedConstruction<android.telecom.PhoneAccountHandle> handles = mockConstruction(android.telecom.PhoneAccountHandle.class);
                 MockedConstruction<android.content.ComponentName> names = mockConstruction(android.content.ComponentName.class);
                 MockedConstruction<android.os.Bundle> bundles = mockConstruction(android.os.Bundle.class);
                 MockedStatic<android.net.Uri> uris = mockStatic(android.net.Uri.class)) {
                android.telecom.PhoneAccount.Builder builder = mock(android.telecom.PhoneAccount.Builder.class, RETURNS_SELF);
                accounts.when(() -> android.telecom.PhoneAccount.builder(any(), any())).thenReturn(builder);
                system.start("old", old);
                system.end(false);
                system.start("new", next);
                CallConnection late = mock(CallConnection.class);
                system.connectionCreated(late);
                verify(next, never()).ready();
                verify(late).end(false);
            }
        }
    }

    @Test public void fallbackWithoutAudioFocusMustNotReportThatTheMicrophoneCanStart() {
        try (MockedStatic<Looper> looper = mockStatic(Looper.class);
             MockedConstruction<Handler> handler = mockConstruction(Handler.class);
             MockedStatic<CallService> services = mockStatic(CallService.class);
             MockedConstruction<android.media.AudioFocusRequest.Builder> builders = mockConstruction(
                 android.media.AudioFocusRequest.Builder.class, withSettings().defaultAnswer(RETURNS_SELF));
             MockedConstruction<android.media.AudioAttributes.Builder> attributes = mockConstruction(
                 android.media.AudioAttributes.Builder.class, withSettings().defaultAnswer(RETURNS_SELF))) {
            Context context = mock(Context.class);
            when(context.getApplicationContext()).thenReturn(context);
            AudioManager audio = mock(AudioManager.class);
            when(context.getSystemService(AudioManager.class)).thenReturn(audio);
            when(audio.requestAudioFocus(any())).thenReturn(AudioManager.AUDIOFOCUS_REQUEST_FAILED);
            NativeCallManager.Host host = mock(NativeCallManager.Host.class);
            NativeCallManager.SystemListener listener = mock(NativeCallManager.SystemListener.class);
            CallSystem system = new CallSystem(context, Runnable::run, host);
            // No Telecom: the real fallback must obtain audio focus before saying ready.
            system.start("Moa", listener);
            verify(listener, never()).ready();
        }
    }
}

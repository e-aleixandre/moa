package com.ealeixandre.moa.app;

import static org.junit.Assert.*;
import static org.mockito.Mockito.*;
import static org.mockito.ArgumentMatchers.*;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.os.Handler;
import android.os.Looper;
import android.telecom.TelecomManager;
import androidx.core.app.Person;
import java.lang.reflect.*;
import java.util.*;
import org.junit.Test;
import org.mockito.MockedStatic;
import org.mockito.MockedConstruction;

public class RecheckAndroidTest {
    private static CallInvite invitation(long now) {
        return CallInvite.from(new PushEnvelope.Content("id", PushEnvelope.Destination.session("sess_1"),
            PushEnvelope.Level.URGENT, "th", "Moa", "b", "call", IncomingCallsTest.INV, now / 1000 + 45));
    }

    @Test public void aPushOfTheNewRegistrationBetweenFenceAndDrainMustNotBeDiscarded() {
        IncomingCallsTest.FakeHost host = new IncomingCallsTest.FakeHost();
        IncomingCalls calls = new IncomingCalls(host);
        // NativeCalls.forgetRinging fences immediately on the auth/FCM thread.
        calls.fence();
        // A completed claim can start the new registration before the call thread drains its old work.
        int freshEpoch = calls.epoch();
        CallInvite fresh = invitation(host.now);
        // The queued NativeCalls.forgetRinging callback runs on the call thread first.
        calls.forgetLocal();
        // Then the new registration's push (which really read freshEpoch before reading its new key).
        calls.receive(fresh, IncomingCallsTest.ORIGIN, "Moa-Voice dev_2.new-key", freshEpoch);
        assertEquals("The same forget must not invalidate the epoch of a new registration twice",
            IncomingCallsTest.INV, calls.ringingInvite());
    }

    @Test public void anIncomingVoiceIdentityMustNotBecomeAWildcardWhenTheDeviceCredentialIsUnreadable() {
        NativeCallManagerTest.FakeHost host = new NativeCallManagerTest.FakeHost();
        NativeCallManager manager = new NativeCallManager(host);
        // Moa-Voice is sufficient for joining an answered invitation; the device credential may
        // have expired or become unreadable since AndroidVoip validated the push before answer.
        host.credential = null;
        assertTrue(manager.openIncoming("Moa", "sess_1", "call_1", NativeCallManagerTest.ORIGIN,
            NativeCallManagerTest.URL, "Moa-Voice dev_1.old-key"));
        host.socket().events.opened();
        host.credential = "dev_2.new-key";
        manager.bindingChanged();
        assertFalse("Claiming dev_2 must end dev_1's call even if the earlier Moa-Device read failed", manager.hasCall());
    }

    private static final class Captured extends RuntimeException {}

    @Test public void notificationHangupsForDifferentCallsMustUseDifferentPendingIntentIdentities() throws Exception {
        List<List<Object>> identities = new ArrayList<>();
        Map<Intent, Map<String,Object>> metadata = new IdentityHashMap<>();
        try (MockedConstruction<Intent> intents = mockConstruction(Intent.class,
                withSettings().defaultAnswer(RETURNS_SELF), (intent, construction) -> {
                    Map<String,Object> fields = new HashMap<>();
                    fields.put("component", construction.arguments().size() > 1 ? construction.arguments().get(1) : null);
                    metadata.put(intent, fields);
                    doAnswer(i -> { fields.put("action", i.getArgument(0)); return intent; }).when(intent).setAction(any());
                    doAnswer(i -> { fields.put("data", i.getArgument(0)); return intent; }).when(intent).setData(any());
                    doAnswer(i -> { fields.put("identifier", i.getArgument(0)); return intent; }).when(intent).setIdentifier(any());
                });
             MockedConstruction<Person.Builder> persons = mockConstruction(Person.Builder.class,
                withSettings().defaultAnswer(RETURNS_SELF));
             MockedStatic<PendingIntent> pending = mockStatic(PendingIntent.class)) {
            pending.when(() -> PendingIntent.getService(any(), anyInt(), any(), anyInt())).thenAnswer(i -> {
                Map<String,Object> f = metadata.get((Intent) i.getArgument(2));
                // Capture the actual API arguments; extras/flags are intentionally NOT part of
                // PendingIntent identity (Android Intent.filterEquals contract).
                identities.add(Arrays.asList(i.getArgument(1), f.get("component"), f.get("action"),
                    f.get("data"), f.get("identifier")));
                throw new Captured(); // Stop before building Android's notification on JVM stubs.
            });
            CallService service = mock(CallService.class);
            Method build = CallService.class.getDeclaredMethod("build", String.class, int.class);
            build.setAccessible(true);
            for (int token : new int[] {0, 1}) {
                try { build.invoke(service, "Moa", token); fail("PendingIntent factory was not reached"); }
                catch (InvocationTargetException e) {
                    assertTrue("Unexpected harness failure: " + e.getCause(), e.getCause() instanceof Captured);
                }
            }
        }
        assertEquals(2, identities.size());
        assertNotEquals("Only the token extra changes; Android reuses and updates the old PendingIntent", identities.get(0), identities.get(1));
    }

    @Test public void anExplicitCurrentTelecomTokenMustWorkOnTheSecondCallToo() {
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
            system.start("old", mock(NativeCallManager.SystemListener.class));
            system.end(false);
            NativeCallManager.SystemListener next = mock(NativeCallManager.SystemListener.class);
            system.start("new", next);
            CallConnection current = mock(CallConnection.class);
            when(current.token()).thenReturn(1);
            system.connectionCreated(current);
            verify(next).ready();
            verify(current, never()).end(anyBoolean());
        }
    }
}

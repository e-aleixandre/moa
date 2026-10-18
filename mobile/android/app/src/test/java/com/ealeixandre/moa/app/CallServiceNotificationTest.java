package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.Mockito.RETURNS_SELF;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.mockConstruction;
import static org.mockito.Mockito.mockStatic;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.mockito.Mockito.withSettings;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import androidx.core.app.NotificationCompat;
import androidx.core.app.Person;
import java.lang.reflect.Method;
import java.util.ArrayList;
import java.util.List;
import org.junit.Test;
import org.mockito.MockedConstruction;
import org.mockito.MockedStatic;

// O1 (owner's decision): the ongoing-call notification shows on the lock
// screen with the owner's name, as CallKit's call does, and carries nothing of
// the conversation, so the call can be hung up without unlocking.
public class CallServiceNotificationTest {

    @Test
    public void theOngoingCallNotificationShowsOnTheLockScreen() throws Exception {
        List<NotificationCompat.Builder> builders = new ArrayList<>();
        List<String> names = new ArrayList<>();
        try (MockedConstruction<Intent> intents = mockConstruction(Intent.class, withSettings().defaultAnswer(RETURNS_SELF));
             MockedConstruction<Person.Builder> persons = mockConstruction(Person.Builder.class,
                 withSettings().defaultAnswer(RETURNS_SELF), (builder, context) -> {
                     org.mockito.Mockito.doAnswer(i -> {
                         names.add(i.getArgument(0));
                         return builder;
                     }).when(builder).setName(any(CharSequence.class));
                 });
             MockedConstruction<NotificationCompat.Builder> notifications = mockConstruction(NotificationCompat.Builder.class,
                 withSettings().defaultAnswer(RETURNS_SELF), (builder, context) -> builders.add(builder));
             MockedStatic<NotificationCompat.CallStyle> styles = mockStatic(NotificationCompat.CallStyle.class);
             MockedStatic<PendingIntent> pending = mockStatic(PendingIntent.class)) {
            CallService service = mock(CallService.class);
            when(service.getString(anyInt())).thenReturn("text");
            Method build = CallService.class.getDeclaredMethod("build", String.class, int.class);
            build.setAccessible(true);
            build.invoke(service, "Atlas", 3);

            assertEquals(1, builders.size());
            // Without this a secure lock screen shows "Contents hidden" and the
            // Hang Up action waits for the unlock.
            verify(builders.get(0)).setVisibility(NotificationCompat.VISIBILITY_PUBLIC);
            assertEquals("Atlas", names.get(0));
        }
    }

    @Test
    public void theChannelDoesNotHideTheNotificationOnTheLockScreen() {
        List<NotificationChannel> channels = new ArrayList<>();
        try (MockedConstruction<NotificationChannel> created = mockConstruction(NotificationChannel.class,
            (channel, context) -> channels.add(channel))) {
            Context context = mock(Context.class);
            NotificationManager manager = mock(NotificationManager.class);
            when(context.getSystemService(NotificationManager.class)).thenReturn(manager);
            when(context.getString(anyInt())).thenReturn("Calls");
            CallService.ensureChannel(context);
            assertEquals(1, channels.size());
            verify(channels.get(0)).setLockscreenVisibility(Notification.VISIBILITY_PUBLIC);
            verify(manager).createNotificationChannel(channels.get(0));
        }
    }
}

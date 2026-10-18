package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.RETURNS_SELF;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.mockConstruction;
import static org.mockito.Mockito.mockStatic;
import static org.mockito.Mockito.when;
import static org.mockito.Mockito.withSettings;

import android.app.PendingIntent;
import android.content.Intent;
import androidx.core.app.NotificationCompat;
import androidx.core.app.Person;
import java.lang.reflect.Method;
import java.util.ArrayList;
import java.util.List;
import org.junit.Test;
import org.mockito.MockedConstruction;
import org.mockito.MockedStatic;

// A public lock-screen notification carries an owner's name and nothing else:
// the page names a call without an owner after its conversation, and that
// title must not reach the lock screen (recheck-fix3). From the page's open
// message to the notification builder, as the real call travels.
public class CallNotificationPrivacyTest {

    private static final String TITLE = "PRIVATE: medical diagnosis discussion";

    private static List<String> personNamesOf(String open) throws Exception {
        NativeCallMessage message = NativeCallMessage.parse(open);
        assertNotNull(message);
        NativeCallManagerTest.FakeHost host = new NativeCallManagerTest.FakeHost();
        NativeCallManager manager = new NativeCallManager(host);
        message.applyTo(manager);
        String systemName = host.system.log.get(0).substring("start ".length());
        List<String> names = new ArrayList<>();
        try (MockedConstruction<Intent> intents = mockConstruction(Intent.class, withSettings().defaultAnswer(RETURNS_SELF));
             MockedConstruction<Person.Builder> persons = mockConstruction(Person.Builder.class,
                 withSettings().defaultAnswer(RETURNS_SELF), (builder, context) ->
                     doAnswer(i -> {
                         names.add(i.getArgument(0).toString());
                         return builder;
                     }).when(builder).setName(any(CharSequence.class)));
             MockedConstruction<NotificationCompat.Builder> builders = mockConstruction(NotificationCompat.Builder.class,
                 withSettings().defaultAnswer(RETURNS_SELF));
             MockedStatic<NotificationCompat.CallStyle> styles = mockStatic(NotificationCompat.CallStyle.class);
             MockedStatic<PendingIntent> pending = mockStatic(PendingIntent.class)) {
            CallService service = mock(CallService.class);
            when(service.getString(R.string.call_ongoing)).thenReturn("Ongoing call");
            Method build = CallService.class.getDeclaredMethod("build", String.class, int.class);
            build.setAccessible(true);
            build.invoke(service, systemName, 3);
        }
        return names;
    }

    @Test
    public void aCallWithoutAnOwnerNeverShowsItsConversationTitle() throws Exception {
        List<String> names = personNamesOf("{\"op\":\"open\",\"id\":\"n1\",\"url\":\"" + NativeCallManagerTest.URL
            + "\",\"name\":\"" + TITLE + "\",\"sessionId\":\"s_private\"}");
        assertFalse("The title reached a public notification: " + names, names.contains(TITLE));
        assertEquals(List.of("moa"), names);
    }

    @Test
    public void anOwnersNameIsShown() throws Exception {
        List<String> names = personNamesOf("{\"op\":\"open\",\"id\":\"n1\",\"url\":\"" + NativeCallManagerTest.URL
            + "\",\"name\":\"Atlas\",\"owner\":true,\"sessionId\":\"s1\"}");
        assertEquals(List.of("Atlas"), names);
    }
}

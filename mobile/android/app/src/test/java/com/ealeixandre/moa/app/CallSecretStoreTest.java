package com.ealeixandre.moa.app;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import org.junit.Test;

// S_v, the incoming-call destination's own secret (iOS IncomingCallSecretStore):
// {origin, device id, 32 bytes} in a sealed box, separate from the alert secret.
public class CallSecretStoreTest {

    static final class MemoryBox implements SecretBox {
        byte[] value;
        boolean failLoad;
        boolean failSave;
        boolean failDelete;

        @Override
        public byte[] load() throws GeneralSecurityException, IOException {
            if (failLoad) throw new GeneralSecurityException("unreadable");
            return value == null ? null : value.clone();
        }

        @Override
        public void save(byte[] plaintext) throws GeneralSecurityException, IOException {
            if (failSave) throw new IOException("disk");
            value = plaintext.clone();
        }

        @Override
        public void delete() throws GeneralSecurityException, IOException {
            if (failDelete) throw new IOException("disk");
            value = null;
        }
    }

    private static byte[] secret(int fill) {
        byte[] s = new byte[32];
        java.util.Arrays.fill(s, (byte) fill);
        return s;
    }

    @Test
    public void roundTripsOriginDeviceAndSecret() {
        MemoryBox box = new MemoryBox();
        CallSecretStore store = new CallSecretStore(box);
        assertNull(store.load());
        assertTrue(store.save(new CallSecretStore.Secret("https://moa.example", "dev_1", secret(9))));
        CallSecretStore.Secret loaded = store.load();
        assertNotNull(loaded);
        assertEquals("https://moa.example", loaded.origin);
        assertEquals("dev_1", loaded.deviceId);
        assertArrayEquals(secret(9), loaded.secret);
    }

    @Test
    public void refusesToSaveASecretThatIsNot32Bytes() {
        CallSecretStore store = new CallSecretStore(new MemoryBox());
        assertFalse(store.save(new CallSecretStore.Secret("https://moa.example", "dev_1", new byte[16])));
        assertNull(store.load());
    }

    @Test
    public void aMalformedValueIsAbsentNotAnError() {
        MemoryBox box = new MemoryBox();
        CallSecretStore store = new CallSecretStore(box);
        box.value = "not json".getBytes(StandardCharsets.UTF_8);
        assertNull(store.load());
        box.value = "{\"origin\":\"https://a\",\"device_id\":\"d\",\"secret\":\"AAAA\"}".getBytes(StandardCharsets.UTF_8);
        assertNull("short secret", store.load());
        box.value = "{\"origin\":\"\",\"device_id\":\"d\",\"secret\":\"".concat(PushEnvelope.base64UrlEncode(secret(1))).concat("\"}")
            .getBytes(StandardCharsets.UTF_8);
        assertNull("empty origin", store.load());
    }

    @Test
    public void anUnreadableBoxLoadsAsNothingAndSaveReportsFailure() {
        MemoryBox box = new MemoryBox();
        CallSecretStore store = new CallSecretStore(box);
        box.failLoad = true;
        assertNull(store.load());
        box.failLoad = false;
        box.failSave = true;
        assertFalse(store.save(new CallSecretStore.Secret("https://moa.example", "dev_1", secret(1))));
    }

    @Test
    public void deleteReportsWhetherTheSecretIsGone() {
        MemoryBox box = new MemoryBox();
        CallSecretStore store = new CallSecretStore(box);
        store.save(new CallSecretStore.Secret("https://moa.example", "dev_1", secret(1)));
        box.failDelete = true;
        assertFalse(store.delete());
        assertNotNull(store.load());
        box.failDelete = false;
        assertTrue(store.delete());
        assertNull(store.load());
    }

    @Test
    public void theVoiceHeaderComesFromTheStoredSecret() {
        CallSecretStore.Secret s = new CallSecretStore.Secret("https://moa.example", "dev_1", secret(3));
        assertEquals(VoiceAuth.header("dev_1", secret(3)), s.authorization());
    }
}

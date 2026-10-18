package com.ealeixandre.moa.app;

import android.content.Context;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.AtomicFile;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.KeyStore;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

// One secret value, encrypted with a non-exportable AES-256-GCM key that lives
// in the Android Keystore, in a file under getNoBackupFilesDir(). The key
// never leaves the Keystore and is not part of any backup or device transfer,
// so a copied file is useless anywhere else -- the Android counterpart of the
// iOS Keychain's *ThisDeviceOnly items. The alias is bound in as AAD so one
// box's file cannot be swapped for another's.
final class KeystoreBox implements SecretBox {

    private static final String KEYSTORE = "AndroidKeyStore";
    private static final byte FORMAT = 1;
    private static final int NONCE_SIZE = 12;

    private final String alias;
    private final AtomicFile file;

    KeystoreBox(Context context, String alias, String fileName) {
        this.alias = alias;
        this.file = new AtomicFile(new File(context.getNoBackupFilesDir(), fileName));
    }

    // Null when nothing is stored. A file whose key is gone (the Keystore was
    // reset) is unreadable for good: it is removed and reported as absent so
    // the app can pair again instead of failing on every launch.
    @Override
    public synchronized byte[] load() throws GeneralSecurityException, IOException {
        if (!file.getBaseFile().exists()) return null;
        KeyStore keyStore = keyStore();
        if (!keyStore.containsAlias(alias)) {
            file.delete();
            return null;
        }
        byte[] blob = file.readFully();
        if (blob.length < 1 + NONCE_SIZE + 16 || blob[0] != FORMAT) {
            throw new GeneralSecurityException("corrupt " + alias);
        }
        SecretKey key = (SecretKey) keyStore.getKey(alias, null);
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.DECRYPT_MODE, key, new GCMParameterSpec(128, blob, 1, NONCE_SIZE));
        cipher.updateAAD(alias.getBytes(StandardCharsets.UTF_8));
        return cipher.doFinal(blob, 1 + NONCE_SIZE, blob.length - 1 - NONCE_SIZE);
    }

    @Override
    public synchronized void save(byte[] plaintext) throws GeneralSecurityException, IOException {
        SecretKey key = keyOrCreate();
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        // The Keystore picks the nonce (randomized encryption is required).
        cipher.init(Cipher.ENCRYPT_MODE, key);
        cipher.updateAAD(alias.getBytes(StandardCharsets.UTF_8));
        byte[] sealed = cipher.doFinal(plaintext);
        byte[] nonce = cipher.getIV();
        if (nonce == null || nonce.length != NONCE_SIZE) throw new GeneralSecurityException("unexpected nonce");
        byte[] blob = new byte[1 + NONCE_SIZE + sealed.length];
        blob[0] = FORMAT;
        System.arraycopy(nonce, 0, blob, 1, NONCE_SIZE);
        System.arraycopy(sealed, 0, blob, 1 + NONCE_SIZE, sealed.length);
        FileOutputStream out = file.startWrite();
        try {
            out.write(blob);
            file.finishWrite(out);
        } catch (IOException e) {
            file.failWrite(out);
            throw e;
        }
    }

    // Removes the file and then the key, so even a file that survived a
    // failed delete can no longer be decrypted.
    @Override
    public synchronized void delete() throws GeneralSecurityException, IOException {
        file.delete();
        if (file.getBaseFile().exists()) throw new IOException("could not delete " + alias);
        KeyStore keyStore = keyStore();
        if (keyStore.containsAlias(alias)) keyStore.deleteEntry(alias);
    }

    private SecretKey keyOrCreate() throws GeneralSecurityException, IOException {
        KeyStore keyStore = keyStore();
        if (keyStore.containsAlias(alias)) return (SecretKey) keyStore.getKey(alias, null);
        KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE);
        generator.init(
            new KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .setRandomizedEncryptionRequired(true)
                .build()
        );
        return generator.generateKey();
    }

    private static KeyStore keyStore() throws GeneralSecurityException, IOException {
        KeyStore keyStore = KeyStore.getInstance(KEYSTORE);
        keyStore.load(null);
        return keyStore;
    }
}

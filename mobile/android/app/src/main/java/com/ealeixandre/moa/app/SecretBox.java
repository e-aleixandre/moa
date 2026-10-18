package com.ealeixandre.moa.app;

import java.io.IOException;
import java.security.GeneralSecurityException;

// One sealed secret value. KeystoreBox is the Android implementation; the
// interface is only so the JVM tests can keep a secret in memory.
interface SecretBox {

    // Null when nothing is stored.
    byte[] load() throws GeneralSecurityException, IOException;

    void save(byte[] plaintext) throws GeneralSecurityException, IOException;

    void delete() throws GeneralSecurityException, IOException;
}

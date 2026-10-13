package com.ealeixandre.moa.app;

import java.util.Map;
import java.util.concurrent.Executor;

// Authenticating a notification tap, off the UI thread. Reading the current
// key may forget an expired pairing, and forgetting waits on the push
// lifecycle, which an incoming message holds around posting its notification
// (PushLifecycle.publish). Only a destination still valid for the same push
// generation and pairing goes back to the UI thread; a tap whose credential
// has expired always ends on the local pairing page instead. Pure Java so the
// JVM tests can drive it.
final class PushTaps {

    // The credential store could not be read.
    static final class Unreadable extends Exception {
    }

    interface Source {
        int generation();

        // May forget an expired pairing, waiting on the push lifecycle.
        PushEnvelope.Keys currentKeys();

        // Whether the stored credential has expired (false when unpaired).
        boolean credentialExpired() throws Unreadable;

        // May load the store: background only.
        String pairedOrigin();

        // From memory only, for the UI thread: the origin last loaded or
        // saved, null when unpaired or not loaded yet.
        String knownOrigin();

        // From memory only: the credential last loaded or saved exists and
        // has not expired.
        boolean knownValid();
    }

    interface Navigation {
        void open(PushEnvelope.Destination destination, String pairedOrigin);

        // Shows the local pairing page.
        void pair();
    }

    private final Executor background;
    private final Executor main;

    PushTaps(Executor background, Executor main) {
        this.background = background;
        this.main = main;
    }

    // Expiry is checked again at tap time: an envelope that is no longer
    // valid leads home, like the fallback it was shown as. A forget or
    // pairing change after the generation is read drops the tap.
    void handle(Map<String, String> envelope, Source source, Navigation navigation) {
        background.execute(() -> {
            int generation = source.generation();
            PushEnvelope.Keys keys = source.currentKeys();
            // Owner decision: an expired credential never leads to the paired
            // origin. The envelope stays closed and the tap shows pairing even
            // if the forget above ended the generation, since that forget is
            // what this outcome is about. Checked after currentKeys: expiry
            // only moves forward, so a credential valid here was valid there.
            if (expired(source)) {
                main.execute(() -> {
                    // A pairing completed since the check (a claim saved a
                    // valid credential) wins. The generation is not compared:
                    // the expiry's own forget above, or a real unpair, moves
                    // it, and both still end on pairing.
                    if (!source.knownValid()) navigation.pair();
                });
                return;
            }
            PushEnvelope.Content content = PushEnvelope.open(envelope, keys, System.currentTimeMillis() / 1000);
            PushEnvelope.Destination destination = content == null ? PushEnvelope.Destination.HOME : content.destination;
            String origin = source.pairedOrigin();
            if (origin == null) return;
            // Memory only on the main thread: the store is never read there.
            main.execute(() -> {
                if (source.generation() != generation || !origin.equals(source.knownOrigin())) return;
                navigation.open(destination, origin);
            });
        });
    }

    // Fails closed: a read error is no proof of a valid credential. The
    // expiry last seen in memory decides; with none, pairing.
    private static boolean expired(Source source) {
        try {
            return source.credentialExpired();
        } catch (Unreadable e) {
            return !source.knownValid();
        }
    }
}

package com.ealeixandre.moa.app;

import java.util.Map;
import java.util.concurrent.Executor;

// Authenticating a notification tap, off the UI thread. Reading the current
// key may forget an expired pairing, and forgetting waits on the push
// lifecycle, which an incoming message holds around posting its notification
// (PushLifecycle.publish). The destination goes back to the UI thread with
// the generation it was authenticated in, for NavigationGate to fence; a tap
// whose credential has expired always ends on the local pairing page
// instead. Pure Java so the JVM tests can drive it.
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

        // From memory only: the credential last loaded or saved exists and
        // has not expired.
        boolean knownValid();
    }

    interface Navigation {
        // `generation`: the push generation the destination was authenticated in.
        void open(PushEnvelope.Destination destination, String pairedOrigin, int generation);

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
    // valid leads home, like the fallback it was shown as.
    void handle(Map<String, String> envelope, Source source, Navigation navigation) {
        background.execute(() -> {
            int generation = source.generation();
            PushEnvelope.Keys keys = source.currentKeys();
            // Owner decision: an expired credential never leads to the paired
            // origin. The envelope stays closed and the tap shows pairing even
            // if the forget above ended the generation, since that forget is
            // what this outcome is about. Checked after currentKeys: expiry
            // only moves forward, so a credential valid here was valid there.
            boolean unreadable = false;
            boolean expired;
            try {
                expired = source.credentialExpired();
            } catch (Unreadable e) {
                // Fails closed: a read error is no proof of a valid
                // credential. The expiry last seen in memory decides; with
                // none, pairing. Even valid in memory, the keys read before
                // the error are not trusted with the envelope: HOME.
                unreadable = true;
                expired = !source.knownValid();
            }
            if (expired) {
                main.execute(() -> {
                    // A pairing completed since the check (a claim saved a
                    // valid credential) wins. The generation is not compared:
                    // the expiry's own forget above, or a real unpair, moves
                    // it, and both still end on pairing.
                    if (!source.knownValid()) navigation.pair();
                });
                return;
            }
            PushEnvelope.Content content = unreadable ? null : PushEnvelope.open(envelope, keys, System.currentTimeMillis() / 1000);
            PushEnvelope.Destination destination = content == null ? PushEnvelope.Destination.HOME : content.destination;
            String origin = source.pairedOrigin();
            main.execute(() -> navigation.open(destination, origin, generation));
        });
    }
}

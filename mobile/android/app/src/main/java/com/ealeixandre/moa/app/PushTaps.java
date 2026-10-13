package com.ealeixandre.moa.app;

import java.util.Map;
import java.util.Objects;
import java.util.concurrent.Executor;

// Authenticating a notification tap, off the UI thread. Reading the current
// key may forget an expired pairing, and forgetting waits on the push
// lifecycle, which an incoming message holds around posting its notification
// (PushLifecycle.publish). Only a destination still valid for the same push
// generation and pairing goes back to the UI thread; a tap whose credential
// has expired always ends on the local pairing page instead. Pure Java so the
// JVM tests can drive it.
final class PushTaps {

    interface Source {
        int generation();

        // May forget an expired pairing, waiting on the push lifecycle.
        PushEnvelope.Keys currentKeys();

        // The stored credential is known to have expired (false when
        // unpaired or unreadable).
        boolean credentialExpired();

        // The same, from cached state only: no Keystore, no lock. Read on
        // the calling (UI) thread.
        boolean knownExpired();

        String pairedOrigin();
    }

    interface Navigation {
        void open(PushEnvelope.Destination destination, String pairedOrigin);

        // Shows the local pairing page.
        void pair();

        // Hides whatever page the WebView holds; pairing lifts it once the
        // local page commits.
        void cover();

        void uncover();
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
    //
    // A tap that is already known to be expired covers the WebView here, on
    // the UI thread before the activity draws again, so the page left open
    // before expiry is never shown. The background check stays the one that
    // decides; if it finds the credential valid after all (replaced since the
    // cache was read), the cover comes off.
    void handle(Map<String, String> envelope, Source source, Navigation navigation) {
        boolean covered = source.knownExpired();
        if (covered) navigation.cover();
        background.execute(() -> {
            int generation = source.generation();
            PushEnvelope.Keys keys = source.currentKeys();
            // Owner decision: an expired credential never leads to the paired
            // origin. The envelope stays closed and the tap shows pairing even
            // if the forget above ended the generation, since that forget is
            // what this outcome is about. Checked after currentKeys: expiry
            // only moves forward, so a credential valid here was valid there.
            if (source.credentialExpired()) {
                main.execute(navigation::pair);
                return;
            }
            PushEnvelope.Content content = PushEnvelope.open(envelope, keys, System.currentTimeMillis() / 1000);
            PushEnvelope.Destination destination = content == null ? PushEnvelope.Destination.HOME : content.destination;
            String origin = source.pairedOrigin();
            main.execute(() -> {
                if (covered) navigation.uncover();
                if (source.generation() != generation || !Objects.equals(origin, source.pairedOrigin())) return;
                navigation.open(destination, origin);
            });
        });
    }
}

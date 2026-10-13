package com.ealeixandre.moa.app;

import java.util.Map;
import java.util.Objects;
import java.util.concurrent.Executor;

// Authenticating a notification tap, off the UI thread. Reading the current
// key may forget an expired pairing, and forgetting waits on the push
// lifecycle, which an incoming message holds around posting its notification
// (PushLifecycle.publish). Only a destination still valid for the same push
// generation and pairing goes back to the UI thread. Pure Java so the JVM
// tests can drive it.
final class PushTaps {

    interface Source {
        int generation();

        // May forget an expired pairing, waiting on the push lifecycle.
        PushEnvelope.Keys currentKeys();

        String pairedOrigin();
    }

    interface Navigation {
        void open(PushEnvelope.Destination destination, String pairedOrigin);
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
            PushEnvelope.Content content = PushEnvelope.open(envelope, keys, System.currentTimeMillis() / 1000);
            PushEnvelope.Destination destination = content == null ? PushEnvelope.Destination.HOME : content.destination;
            String origin = source.pairedOrigin();
            main.execute(() -> {
                if (source.generation() != generation || !Objects.equals(origin, source.pairedOrigin())) return;
                navigation.open(destination, origin);
            });
        });
    }
}

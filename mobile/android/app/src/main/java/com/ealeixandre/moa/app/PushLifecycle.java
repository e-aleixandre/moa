package com.ealeixandre.moa.app;

import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.BooleanSupplier;
import java.util.function.Supplier;

// The order between ending a push generation (forgetting S, STATE_* and every
// shown notification) and the local effects that depend on it: a
// registration's S/STATE_* writes and an incoming message's post. Its monitor
// is held only for that local work, never across network or token waits.
// Pure Java so the JVM tests can interleave it.
final class PushLifecycle {

    private final AtomicInteger generation = new AtomicInteger();

    int generation() {
        return generation.get();
    }

    // Ends the current generation and forgets in one step: whatever an older
    // generation checks under this monitor afterwards sees it ended.
    synchronized <T> T end(Supplier<T> forget) {
        generation.incrementAndGet();
        return forget.get();
    }

    // Posts only if no generation ended since `seen` and push is still on,
    // in the same step, so a post can never land after a completed forget.
    synchronized boolean publish(int seen, BooleanSupplier on, Runnable post) {
        if (generation.get() != seen || !on.getAsBoolean()) return false;
        post.run();
        return true;
    }
}

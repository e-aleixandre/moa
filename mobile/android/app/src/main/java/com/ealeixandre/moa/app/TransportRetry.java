package com.ealeixandre.moa.app;

// Retrying the paired page after a transport failure, with backoff. Main
// thread only; the host owns the timer (a Handler) and the actual load. Pure
// Java so the JVM tests can drive it.
final class TransportRetry {

    interface Host {
        void postDelayed(Runnable task, long delayMillis);

        void removeCallbacks(Runnable task);

        // Loads url again; false when it is no longer the paired page.
        boolean reload(String url);
    }

    private static final long[] DELAYS = { 1_000, 2_000, 4_000, 8_000, 15_000 };

    private final Host host;
    private String url;
    private int index;
    private Runnable task;

    TransportRetry(Host host) {
        this.host = host;
    }

    String url() {
        return url;
    }

    // A main-frame transport failure loading url on the paired origin.
    void failed(String failedUrl) {
        if (!failedUrl.equals(url)) {
            url = failedUrl;
            index = 0;
        }
        schedule();
    }

    // The app is about to load target in the main frame itself. Going to
    // the local (pairing) page ends the retry: a pending one would replace
    // that page with the old paired one. Same thread as the timer, so a
    // cancelled retry cannot still run.
    void navigating(String target) {
        if (OriginPolicy.isLocal(target)) stop();
    }

    // Retries at once (the app came back, the network returned).
    void now(boolean cancelScheduled) {
        if (url == null) return;
        if (cancelScheduled && task != null) {
            host.removeCallbacks(task);
            task = null;
        }
        if (!host.reload(url)) stop();
    }

    void stop() {
        if (task != null) host.removeCallbacks(task);
        task = null;
        url = null;
        index = 0;
    }

    private void schedule() {
        if (task != null || url == null) return;
        long delay = DELAYS[Math.min(index, DELAYS.length - 1)];
        index = Math.min(index + 1, DELAYS.length - 1);
        task = () -> {
            task = null;
            now(false);
        };
        host.postDelayed(task, delay);
    }
}

package com.ealeixandre.moa.app;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import com.ealeixandre.moa.app.ShareInbox.InboxException.Code;
import java.io.ByteArrayInputStream;
import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import org.json.JSONObject;
import org.junit.Before;
import org.junit.Rule;
import org.junit.Test;
import org.junit.rules.TemporaryFolder;

public class ShareInboxTest {

    @Rule
    public TemporaryFolder folder = new TemporaryFolder();

    private File root;
    private ShareInbox inbox;

    @Before
    public void setUp() throws IOException {
        root = new File(folder.getRoot(), "ShareInbox");
        inbox = new ShareInbox(root);
    }

    private static InputStream stream(String content) {
        return new ByteArrayInputStream(content.getBytes(StandardCharsets.UTF_8));
    }

    // Produces size bytes without holding them, and fails if read before the
    // size check was supposed to stop it.
    private static InputStream generated(long size, boolean mayRead) {
        return new InputStream() {
            long left = size;

            @Override
            public int read() {
                if (!mayRead) throw new AssertionError("read before the size check");
                if (left == 0) return -1;
                left--;
                return 'x';
            }

            @Override
            public int read(byte[] buffer, int offset, int length) {
                if (!mayRead) throw new AssertionError("read before the size check");
                if (left == 0) return -1;
                int n = (int) Math.min(length, left);
                left -= n;
                return n;
            }
        };
    }

    private static Code code(ThrowingRunnable action) throws IOException {
        try {
            action.run();
        } catch (ShareInbox.InboxException e) {
            return e.code;
        }
        fail("expected an inbox error");
        return null;
    }

    private interface ThrowingRunnable {
        void run() throws IOException, ShareInbox.InboxException;
    }

    private String[] stagingDirs() {
        String[] names = root.list((dir, name) -> name.endsWith(".tmp"));
        return names == null ? new String[0] : names;
    }

    @Test
    public void aCommittedShareIsPeekedOldestFirstAndAcknowledged() throws Exception {
        ShareInbox.Transaction first = inbox.begin();
        first.addFile(stream("PNG"), "../photo.png", "image/png", 3);
        first.commit(" Page ", "", " https://a.example ");
        Thread.sleep(5);
        ShareInbox.Transaction second = inbox.begin();
        second.commit("", "second", "");
        assertEquals(2, inbox.pendingCount());
        assertEquals(0, stagingDirs().length);

        ShareInbox.Envelope next = inbox.next();
        assertTrue(ShareInbox.isId(next.id));
        assertEquals("Page", next.title);
        assertEquals("https://a.example", next.url);
        assertEquals(1, next.files.size());
        assertEquals("photo.png", next.files.get(0).name);
        assertEquals("image/png", next.files.get(0).mime);
        assertEquals(3, next.files.get(0).size);
        assertArrayEquals("PNG".getBytes(StandardCharsets.UTF_8),
            Files.readAllBytes(new File(next.directory, next.files.get(0).storedName).toPath()));

        // The iOS manifest shape.
        JSONObject manifest = new JSONObject(new String(Files.readAllBytes(new File(next.directory, "manifest.json").toPath()), StandardCharsets.UTF_8));
        for (String key : new String[] {"id", "createdAt", "title", "text", "url", "files"}) assertTrue(key, manifest.has(key));
        assertTrue(manifest.getJSONArray("files").getJSONObject(0).has("storedName"));

        inbox.acknowledge(next.id);
        assertEquals(1, inbox.pendingCount());
        assertEquals("second", inbox.next().text);
        assertEquals(Code.CORRUPT, code(() -> inbox.acknowledge("../x")));
    }

    @Test
    public void anEmptyInboxPeeksNothing() throws Exception {
        assertNull(inbox.next());
        assertEquals(0, inbox.pendingCount());
    }

    @Test
    public void aDiscardedShareLeavesNothing() throws Exception {
        ShareInbox.Transaction transaction = inbox.begin();
        transaction.addFile(stream("x"), "a.txt", "text/plain", 1);
        assertEquals(1, stagingDirs().length);
        transaction.discard();
        assertEquals(0, stagingDirs().length);
        assertEquals(0, inbox.pendingCount());
    }

    @Test
    public void atMostEightFiles() throws Exception {
        ShareInbox.Transaction transaction = inbox.begin();
        for (int i = 0; i < 8; i++) transaction.addFile(stream("x"), "f" + i, null, 1);
        assertEquals(Code.TOO_MANY_FILES, code(() -> transaction.addFile(stream("x"), "f8", null, 1)));
        assertEquals("application/octet-stream", transaction.files().get(0).mime);
    }

    @Test
    public void aDeclaredSizeOverTheLimitIsRefusedBeforeReading() throws Exception {
        ShareInbox.Transaction transaction = inbox.begin();
        assertEquals(Code.TOO_LARGE, code(() -> transaction.addFile(generated(0, false), "big", null, ShareLogic.MAX_BYTES + 1)));
        transaction.addFile(generated(ShareLogic.MAX_BYTES - 10, true), "most", null, -1);
        assertEquals(Code.TOO_LARGE, code(() -> transaction.addFile(generated(0, false), "more", null, 11)));
    }

    @Test
    public void aProviderThatUnderReportsCannotExceedTheLimit() throws Exception {
        ShareInbox.Transaction transaction = inbox.begin();
        assertEquals(Code.TOO_LARGE, code(() -> transaction.addFile(generated(ShareLogic.MAX_BYTES + 1, true), "liar", null, 10)));
        assertTrue(transaction.files().isEmpty());
        File staging = new File(root, stagingDirs()[0]);
        assertEquals(0, staging.list().length);
    }

    @Test
    public void textLimitAndEmptyShares() throws Exception {
        StringBuilder big = new StringBuilder();
        for (int i = 0; i < ShareLogic.MAX_TEXT_BYTES; i++) big.append('a');
        assertEquals(Code.TEXT_TOO_LARGE, code(() -> inbox.begin().commit("t", big.toString(), "")));
        assertEquals(Code.EMPTY, code(() -> inbox.begin().commit(" ", "\n", "")));
    }

    @Test
    public void atMostTwentyWaiting() throws Exception {
        for (int i = 0; i < ShareLogic.MAX_PENDING; i++) inbox.begin().commit("", "share " + i, "");
        assertEquals(Code.QUEUE_FULL, code(() -> inbox.begin().commit("", "one more", "")));
        assertEquals(ShareLogic.MAX_PENDING, inbox.pendingCount());
    }

    @Test
    public void staleStagingIsSweptAndFreshStagingKept() throws Exception {
        root.mkdirs();
        File stale = new File(root, ".00000000-0000-4000-8000-000000000000.tmp");
        File fresh = new File(root, ".00000000-0000-4000-8000-000000000001.tmp");
        assertTrue(stale.mkdir());
        assertTrue(fresh.mkdir());
        assertTrue(stale.setLastModified(System.currentTimeMillis() - 2 * 60 * 60 * 1000));
        inbox.begin();
        assertFalse(stale.exists());
        assertTrue(fresh.exists());
    }

    @Test
    public void aManifestForAnotherIdIsCorrupt() throws Exception {
        inbox.begin().commit("", "x", "");
        File directory = inbox.next().directory;
        File manifest = new File(directory, "manifest.json");
        String text = new String(Files.readAllBytes(manifest.toPath()), StandardCharsets.UTF_8)
            .replace(directory.getName(), "00000000-0000-4000-8000-000000000000");
        Files.write(manifest.toPath(), text.getBytes(StandardCharsets.UTF_8));
        assertEquals(Code.CORRUPT, code(() -> inbox.next()));
    }
}

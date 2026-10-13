package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertSame;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import org.json.JSONException;
import org.junit.Test;

public class ShareLogicTest {

    // The cases of shareComposerText in pkg/serve/frontend/src/data/share-target.test.js.
    @Test
    public void aSharedLinkIsNotPrintedThreeTimes() {
        assertEquals("https://example.com/api\nWeb Share Target API",
            ShareLogic.composedText("Web Share Target API", "https://example.com/api", "https://example.com/api"));
    }

    @Test
    public void sharedTextUrlAndTitleEachSurviveWhenTheyDiffer() {
        assertEquals("look at this\nhttps://x.test/p\nA post",
            ShareLogic.composedText("A post", "look at this", "https://x.test/p"));
        assertEquals("", ShareLogic.composedText("", "", ""));
        assertEquals("", ShareLogic.composedText(null, null, null));
    }

    // The iOS ShareLogicTests cases.
    @Test
    public void composedDeduplicatesOneLinePerValue() {
        assertEquals("Read https://a.example/x\nPage",
            ShareLogic.composedText("Page", "Read https://a.example/x", "https://a.example/x"));
        assertEquals("https://a.example", ShareLogic.composedText("https://a.example", "", "https://a.example"));
        assertEquals("", ShareLogic.composedText(" ", "\n", ""));
    }

    @Test
    public void messagePutsTheNoteFirstAndSkipsAnEmptyNote() {
        assertEquals("léelo\nhttps://a.example", ShareLogic.message(" léelo ", "", "", "https://a.example"));
        assertEquals("https://a.example", ShareLogic.message("  ", "", "", "https://a.example"));
        assertEquals("hi", ShareLogic.message("hi", "", "", ""));
        assertEquals("", ShareLogic.message(null, "", "", ""));
    }

    @Test
    public void summary() {
        assertEquals("a.png", ShareLogic.summary(Collections.singletonList("a.png"), "1 file", "", "", ""));
        assertEquals("2 files · a.png, b.pdf", ShareLogic.summary(Arrays.asList("a.png", "b.pdf"), "2 files", "", "", ""));
        StringBuilder words = new StringBuilder();
        for (int i = 0; i < 40; i++) words.append("word ");
        String line = ShareLogic.summary(Collections.emptyList(), "", "", words.toString(), "");
        assertEquals(80, line.codePointCount(0, line.length()));
        assertTrue(line.endsWith("…"));
        assertEquals("a b", ShareLogic.summary(Collections.emptyList(), "", "", "a\n\n b", ""));
    }

    @Test
    public void parsingIgnoresUnknownFieldsKindsAndUnsafeIdsAndKeepsTheServerOrder() throws JSONException {
        String json = "{\"extra\":1,\"destinations\":["
            + "{\"kind\":\"owner\",\"session_id\":\"o1\",\"title\":\"moa\",\"project\":\"moa\",\"state\":\"idle\",\"updated\":\"x\",\"avatar\":\"a\"},"
            + "{\"kind\":\"owner\",\"session_id\":\"o2\",\"title\":\"winerim\",\"project\":\"wine\",\"state\":\"saved\"},"
            + "{\"kind\":\"session\",\"session_id\":\"s-2\",\"title\":\"\",\"project\":\"\",\"state\":\"saved\"},"
            + "{\"kind\":\"hologram\",\"session_id\":\"h1\",\"title\":\"?\"},"
            + "{\"kind\":\"session\",\"session_id\":\"../x\",\"title\":\"bad\"},"
            + "{\"kind\":\"session\",\"session_id\":\"a/b\",\"title\":\"bad\"},"
            + "{\"kind\":\"session\",\"session_id\":\"..\",\"title\":\"bad\"},"
            + "{\"kind\":\"session\",\"session_id\":7,\"title\":\"bad\"},"
            + "{\"kind\":\"session\",\"title\":\"no id\"},"
            + "\"not an object\","
            + "{\"kind\":\"session\",\"session_id\":\"s_3.x\",\"title\":5}"
            + "]}";
        List<ShareLogic.Destination> list = ShareLogic.parseDestinations(json);
        assertEquals(4, list.size());
        assertEquals("o1", list.get(0).sessionId);
        assertEquals(ShareLogic.Kind.OWNER, list.get(0).kind);
        assertEquals("moa", list.get(0).title);
        assertFalse(list.get(0).closed());
        assertEquals("o2", list.get(1).sessionId);
        assertTrue(list.get(1).closed());
        assertEquals("s-2", list.get(2).sessionId);
        assertEquals(ShareLogic.Kind.SESSION, list.get(2).kind);
        assertEquals("", list.get(2).title);
        assertEquals("s_3.x", list.get(3).sessionId);
        // A non-string title reads as no title, not as "5".
        assertEquals("", list.get(3).title);
    }

    @Test
    public void parsingRejectsTheWrongShape() {
        for (String json : new String[] {"[]", "{}", "{\"destinations\":{}}", "not json"}) {
            try {
                ShareLogic.parseDestinations(json);
                fail(json);
            } catch (JSONException expected) {
                // ok
            }
        }
    }

    @Test
    public void filterMatchesTitleOrProjectCaseInsensitively() {
        List<ShareLogic.Destination> list = Arrays.asList(
            new ShareLogic.Destination(ShareLogic.Kind.OWNER, "1", "Winerim", "wine", "idle"),
            new ShareLogic.Destination(ShareLogic.Kind.SESSION, "2", "Fix", "API", "saved"));
        assertSame(list, ShareLogic.filter(list, "  "));
        assertEquals("1", ShareLogic.filter(list, "WINE").get(0).sessionId);
        assertEquals(1, ShareLogic.filter(list, "WINE").size());
        assertEquals("2", ShareLogic.filter(list, "api").get(0).sessionId);
        assertTrue(ShareLogic.filter(list, "zzz").isEmpty());
    }

    @Test
    public void aTextThatIsOneLinkIsTheSharedUrl() {
        assertTrue(ShareLogic.isSingleUrl(" https://example.com/a?b=1 "));
        assertTrue(ShareLogic.isSingleUrl("http://example.com"));
        assertFalse(ShareLogic.isSingleUrl("read https://example.com"));
        assertFalse(ShareLogic.isSingleUrl("https://a.example\nhttps://b.example"));
        assertFalse(ShareLogic.isSingleUrl("ftp://example.com"));
        assertFalse(ShareLogic.isSingleUrl(""));
        assertFalse(ShareLogic.isSingleUrl(null));
    }

    @Test
    public void fileNamesAreOnlyTheLastPathElement() {
        assertEquals("a.png", ShareLogic.safeFileName("a.png"));
        assertEquals("passwd", ShareLogic.safeFileName("../../etc/passwd"));
        assertEquals("x.txt", ShareLogic.safeFileName("C:\\tmp\\x.txt"));
        assertEquals("shared-file", ShareLogic.safeFileName(".."));
        assertEquals("shared-file", ShareLogic.safeFileName("dir/"));
        assertEquals("shared-file", ShareLogic.safeFileName(null));
    }

    @Test
    public void limitsMatchIos() {
        assertEquals(8, ShareLogic.MAX_FILES);
        assertEquals(32L * 1024 * 1024, ShareLogic.MAX_BYTES);
        assertEquals(256 * 1024, ShareLogic.MAX_TEXT_BYTES);
        assertEquals(20, ShareLogic.MAX_PENDING);
        // Measured on the composed text: a title the text already contains
        // is not counted twice.
        String text = "é".repeat(ShareLogic.MAX_TEXT_BYTES / 2);
        assertTrue(ShareLogic.textFits(text, text, ""));
        assertFalse(ShareLogic.textFits("t", text, ""));
    }

    // Review S2: the 262144-byte limit applies to exactly the text sent,
    // note + separator + shared text, not only to the shared part.
    @Test
    public void theLimitIsMeasuredOnTheTextThatIsUploaded() {
        String note = "n".repeat(ShareLogic.MAX_TEXT_BYTES);
        assertEquals(null, ShareLogic.uploadText(note, "", "x", ""));
        String fits = "n".repeat(ShareLogic.MAX_TEXT_BYTES - 2);
        assertEquals(fits + "\nx", ShareLogic.uploadText(fits, "", "x", ""));
        assertEquals(null, ShareLogic.uploadText("n".repeat(ShareLogic.MAX_TEXT_BYTES - 1), "", "x", ""));
    }

    // Review S2: saving keeps the same content as sending would (the note
    // too), under the same limit.
    @Test
    public void savingKeepsTheNoteUnderTheSameLimit() {
        assertEquals("read this\nshared", ShareLogic.savedText("  read this ", "T", "shared", "https://x.test"));
        assertEquals("shared", ShareLogic.savedText(" ", "T", "shared", ""));
        assertEquals(null, ShareLogic.savedText("n".repeat(ShareLogic.MAX_TEXT_BYTES), "", "x", ""));
    }

    // Review S1 / contract "Uncertain sends": a send that may have reached the
    // server is never saved or retried; Save for later only when it certainly
    // did not arrive.
    @Test
    public void anUncertainSendOffersNeitherSaveNorRetry() {
        List<ShareLogic.FailureAction> uncertain = ShareLogic.failureActions(ShareClient.ShareException.Kind.OUTCOME_UNKNOWN);
        assertFalse(uncertain.contains(ShareLogic.FailureAction.SAVE));
        assertFalse(uncertain.contains(ShareLogic.FailureAction.RETRY));
        assertTrue(uncertain.contains(ShareLogic.FailureAction.CLOSE));
        for (ShareClient.ShareException.Kind kind : ShareClient.ShareException.Kind.values()) {
            if (kind == ShareClient.ShareException.Kind.OUTCOME_UNKNOWN) continue;
            assertTrue(kind.name(), ShareLogic.failureActions(kind).contains(ShareLogic.FailureAction.SAVE));
        }
    }
}

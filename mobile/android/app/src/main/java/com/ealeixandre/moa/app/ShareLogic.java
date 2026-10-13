package com.ealeixandre.moa.app;

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

// The pure parts of sharing into moa, pinned by JVM tests: the limits, the
// text that is sent, and the destinations list. Port of iOS ShareLogic.swift
// and of shareComposerText in the web frontend (data/share-target.js).
final class ShareLogic {

    // Same limits as iOS SharedInbox and the frontend's native-share.js, which
    // refuses a queued share over 8 files or 32 MB in total.
    static final int MAX_FILES = 8;
    static final long MAX_BYTES = 32L * 1024 * 1024;
    static final int MAX_TEXT_BYTES = 256 * 1024;
    static final int MAX_PENDING = 20;

    enum Kind {
        OWNER,
        SESSION
    }

    static final class Destination {
        final Kind kind;
        final String sessionId;
        final String title;
        final String project;
        final String state;

        Destination(Kind kind, String sessionId, String title, String project, String state) {
            this.kind = kind;
            this.sessionId = sessionId;
            this.title = title;
            this.project = project;
            this.state = state;
        }

        boolean closed() {
            return "saved".equals(state);
        }
    }

    private ShareLogic() {}

    // shareComposerText: text, url, then title, trimmed, one per line, each
    // dropped when an earlier line already contains it.
    static String composedText(String title, String text, String url) {
        List<String> lines = new ArrayList<>();
        for (String value : new String[] {text, url, title}) {
            String trimmed = value == null ? "" : value.trim();
            if (trimmed.isEmpty()) continue;
            boolean seen = false;
            for (String line : lines) {
                if (line.contains(trimmed)) {
                    seen = true;
                    break;
                }
            }
            if (!seen) lines.add(trimmed);
        }
        return String.join("\n", lines);
    }

    // What /send receives: the optional note first, then the shared text.
    static String message(String note, String title, String text, String url) {
        List<String> parts = new ArrayList<>();
        String cleanNote = note == null ? "" : note.trim();
        if (!cleanNote.isEmpty()) parts.add(cleanNote);
        String shared = composedText(title, text, url);
        if (!shared.isEmpty()) parts.add(shared);
        return String.join("\n", parts);
    }

    // The text /send receives (message), or null when it is over
    // MAX_TEXT_BYTES: the limit applies to exactly what is uploaded, note
    // and separator included.
    static String uploadText(String note, String title, String text, String url) {
        String message = message(note, title, text, url);
        return fits(message) ? message : null;
    }

    // The text "Save for later" stores: the note first, then the shared text,
    // so moa's picker places the same content the sheet would have sent
    // (title and URL stay in their own fields). Null when what the composer
    // would get from it is over MAX_TEXT_BYTES.
    static String savedText(String note, String title, String text, String url) {
        List<String> parts = new ArrayList<>();
        String cleanNote = note == null ? "" : note.trim();
        if (!cleanNote.isEmpty()) parts.add(cleanNote);
        if (text != null && !text.trim().isEmpty()) parts.add(text);
        String saved = String.join("\n", parts);
        return fits(composedText(title, saved, url)) ? saved : null;
    }

    private static boolean fits(String value) {
        return value.getBytes(StandardCharsets.UTF_8).length <= MAX_TEXT_BYTES;
    }

    enum FailureAction {
        OPEN_MOA,
        SAVE,
        RETRY,
        CHOOSE_ANOTHER,
        CLOSE
    }

    // The buttons after a failed list or send, primary first. Save for later
    // only when the share certainly did not arrive; a send that may have
    // reached the server is neither saved nor retried, or it could arrive
    // twice (contract "Uncertain sends").
    static List<FailureAction> failureActions(ShareClient.ShareException.Kind kind) {
        switch (kind) {
            case NOT_PAIRED:
                return Arrays.asList(FailureAction.OPEN_MOA, FailureAction.SAVE);
            case OUTCOME_UNKNOWN:
                return Arrays.asList(FailureAction.OPEN_MOA, FailureAction.CLOSE);
            case NOT_FOUND:
                return Arrays.asList(FailureAction.CHOOSE_ANOTHER, FailureAction.SAVE);
            default:
                return Arrays.asList(FailureAction.RETRY, FailureAction.SAVE);
        }
    }

    // One line describing the share. manyFiles is the localized "N files"
    // label, used only when there is more than one file.
    static String summary(List<String> fileNames, String manyFiles, String title, String text, String url) {
        if (fileNames.size() == 1) return fileNames.get(0);
        if (fileNames.size() > 1) return manyFiles + " · " + String.join(", ", fileNames);
        String flat = composedText(title, text, url).trim().replaceAll("\\s+", " ");
        if (flat.codePointCount(0, flat.length()) <= 80) return flat;
        return flat.substring(0, flat.offsetByCodePoints(0, 79)) + "…";
    }

    // The bytes the queue counts against MAX_TEXT_BYTES, as iOS commit() does.
    static int textBytes(String title, String text, String url) {
        int total = 0;
        for (String value : new String[] {title, text, url}) {
            if (value != null) total += value.trim().getBytes(StandardCharsets.UTF_8).length;
        }
        return total;
    }

    // Android has no separate URL extra: a text that is exactly one http(s)
    // URL is treated as the shared URL, like Safari's on iOS.
    static boolean isSingleUrl(String value) {
        if (value == null) return false;
        String trimmed = value.trim();
        if (trimmed.isEmpty() || trimmed.matches(".*\\s.*")) return false;
        String lower = trimmed.toLowerCase(Locale.ROOT);
        return lower.startsWith("https://") || lower.startsWith("http://");
    }

    // A queued or sent file name: the last path element, never empty.
    static String safeFileName(String name) {
        String value = name == null ? "" : name.trim();
        int slash = Math.max(value.lastIndexOf('/'), value.lastIndexOf('\\'));
        if (slash >= 0) value = value.substring(slash + 1);
        if (value.isEmpty() || value.equals(".") || value.equals("..")) return "shared-file";
        return value;
    }

    // GET /api/share/destinations. Unknown kinds, unknown fields and ids that
    // could change the request path are skipped; the server's order (owners
    // first) is kept as given.
    static List<Destination> parseDestinations(String json) throws JSONException {
        JSONObject root = new JSONObject(json);
        JSONArray rows = root.getJSONArray("destinations");
        List<Destination> out = new ArrayList<>();
        for (int i = 0; i < rows.length(); i++) {
            JSONObject row = rows.optJSONObject(i);
            if (row == null) continue;
            Kind kind;
            String rawKind = row.optString("kind", "");
            if ("owner".equals(rawKind)) kind = Kind.OWNER;
            else if ("session".equals(rawKind)) kind = Kind.SESSION;
            else continue;
            Object id = row.opt("session_id");
            if (!(id instanceof String) || !isPathSafe((String) id)) continue;
            out.add(new Destination(kind, (String) id, string(row, "title"), string(row, "project"), string(row, "state")));
        }
        return Collections.unmodifiableList(out);
    }

    static List<Destination> filter(List<Destination> destinations, String query) {
        String needle = query == null ? "" : query.trim().toLowerCase(Locale.ROOT);
        if (needle.isEmpty()) return destinations;
        List<Destination> out = new ArrayList<>();
        for (Destination destination : destinations) {
            if (destination.title.toLowerCase(Locale.ROOT).contains(needle)
                || destination.project.toLowerCase(Locale.ROOT).contains(needle)) {
                out.add(destination);
            }
        }
        return out;
    }

    static boolean isPathSafe(String id) {
        if (id == null || id.isEmpty() || id.equals(".") || id.equals("..")) return false;
        for (int i = 0; i < id.length(); i++) {
            char c = id.charAt(i);
            boolean ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
                || c == '-' || c == '_' || c == '.';
            if (!ok) return false;
        }
        return true;
    }

    private static String string(JSONObject row, String key) {
        Object value = row.opt(key);
        return value instanceof String ? (String) value : "";
    }
}

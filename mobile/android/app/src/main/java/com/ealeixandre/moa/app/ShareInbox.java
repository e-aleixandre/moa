package com.ealeixandre.moa.app;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.UUID;
import java.util.regex.Pattern;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

// Shares kept for later, placed from moa's own picker through
// window.MoaShareInbox. Port of iOS SharedInbox.swift with the same manifest
// and limits; it lives in the no-backup directory instead of an App Group.
// A share is staged in ".<id>.tmp" while the share sheet is open (the content
// URI grant ends with that activity) and becomes visible to the app only when
// commit() renames it to "<id>".
final class ShareInbox {

    static final class InboxException extends Exception {
        enum Code {
            QUEUE_FULL,
            TOO_MANY_FILES,
            TOO_LARGE,
            TEXT_TOO_LARGE,
            EMPTY,
            CORRUPT
        }

        final Code code;

        InboxException(Code code) {
            super(code.name());
            this.code = code;
        }

        InboxException(Code code, Throwable cause) {
            super(code.name(), cause);
            this.code = code;
        }
    }

    static final class StoredFile {
        final String name;
        final String mime;
        final long size;
        final String storedName;

        StoredFile(String name, String mime, long size, String storedName) {
            this.name = name;
            this.mime = mime;
            this.size = size;
            this.storedName = storedName;
        }
    }

    static final class Envelope {
        final String id;
        final double createdAt;
        final String title;
        final String text;
        final String url;
        final List<StoredFile> files;
        final File directory;

        Envelope(String id, double createdAt, String title, String text, String url, List<StoredFile> files, File directory) {
            this.id = id;
            this.createdAt = createdAt;
            this.title = title;
            this.text = text;
            this.url = url;
            this.files = files;
            this.directory = directory;
        }
    }

    private static final String MANIFEST = "manifest.json";
    // The largest manifest commit() writes for text textFits admits, and
    // the most read() accepts. Title, text and URL are each at most
    // MAX_TEXT_BYTES (a field the composition drops is inside one it keeps),
    // and JSON escaping takes at most six bytes per byte (a control character
    // becomes a six-character unicode escape); the slack is the id,
    // timestamp, keys and file rows.
    private static final int MAX_JSON_ESCAPE = 6;
    private static final long MANIFEST_SLACK_BYTES = 64L * 1024;
    private static final long MAX_MANIFEST_BYTES = 3L * MAX_JSON_ESCAPE * ShareLogic.MAX_TEXT_BYTES + MANIFEST_SLACK_BYTES;
    private static final Pattern ID = Pattern.compile("^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$");
    // A staging directory older than this belongs to a share sheet that died.
    private static final long STALE_STAGING_MILLIS = 60L * 60 * 1000;

    private final File root;

    ShareInbox(File root) {
        this.root = root;
    }

    static ShareInbox in(android.content.Context context) {
        return new ShareInbox(new File(context.getNoBackupFilesDir(), "ShareInbox"));
    }

    static boolean isId(String value) {
        return value != null && ID.matcher(value).matches();
    }

    int pendingCount() throws IOException {
        return committed().size();
    }

    Envelope next() throws IOException, InboxException {
        Envelope oldest = null;
        for (File directory : committed()) {
            Envelope envelope = read(directory);
            if (oldest == null || envelope.createdAt < oldest.createdAt) oldest = envelope;
        }
        return oldest;
    }

    void acknowledge(String id) throws IOException, InboxException {
        if (!isId(id)) throw new InboxException(InboxException.Code.CORRUPT);
        File directory = new File(root, id);
        if (directory.exists()) deleteTree(directory);
    }

    Transaction begin() throws IOException {
        ensureRoot();
        sweepStaleStaging(System.currentTimeMillis());
        return new Transaction();
    }

    final class Transaction {
        private final String id = UUID.randomUUID().toString().toLowerCase(Locale.ROOT);
        private final File staging = new File(root, "." + id + ".tmp");
        private final List<StoredFile> files = new ArrayList<>();
        private long totalBytes;
        private boolean finished;

        private Transaction() throws IOException {
            if (!staging.mkdir()) throw new IOException("cannot create staging directory");
        }

        List<StoredFile> files() {
            return Collections.unmodifiableList(files);
        }

        File fileFor(StoredFile file) {
            return new File(staging, file.storedName);
        }

        // Copies one shared file. declaredSize (-1 when unknown) is checked
        // before reading anything; the bytes actually read are checked as they
        // arrive, so a provider that under-reports cannot exceed the limit.
        void addFile(InputStream input, String name, String mime, long declaredSize) throws IOException, InboxException {
            long remaining = ShareLogic.MAX_BYTES - totalBytes;
            if (files.size() >= ShareLogic.MAX_FILES || declaredSize > remaining) {
                input.close();
                throw new InboxException(files.size() >= ShareLogic.MAX_FILES
                    ? InboxException.Code.TOO_MANY_FILES : InboxException.Code.TOO_LARGE);
            }
            String storedName = UUID.randomUUID().toString().toLowerCase(Locale.ROOT);
            File destination = new File(staging, storedName);
            long size = 0;
            try (InputStream in = input; OutputStream out = new FileOutputStream(destination)) {
                byte[] buffer = new byte[64 * 1024];
                int n;
                while ((n = in.read(buffer)) != -1) {
                    size += n;
                    if (size > remaining) throw new InboxException(InboxException.Code.TOO_LARGE);
                    out.write(buffer, 0, n);
                }
            } catch (InboxException | IOException e) {
                destination.delete();
                throw e;
            }
            String cleanMime = mime == null || mime.trim().isEmpty() ? "application/octet-stream" : mime.trim();
            files.add(new StoredFile(ShareLogic.safeFileName(name), cleanMime, size, storedName));
            totalBytes += size;
        }

        void commit(String title, String text, String url) throws IOException, InboxException {
            if (finished) throw new InboxException(InboxException.Code.CORRUPT);
            String cleanTitle = title == null ? "" : title.trim();
            String cleanText = text == null ? "" : text.trim();
            String cleanUrl = url == null ? "" : url.trim();
            if (!ShareLogic.textFits(cleanTitle, cleanText, cleanUrl)) {
                throw new InboxException(InboxException.Code.TEXT_TOO_LARGE);
            }
            if (cleanTitle.isEmpty() && cleanText.isEmpty() && cleanUrl.isEmpty() && files.isEmpty()) {
                throw new InboxException(InboxException.Code.EMPTY);
            }
            if (pendingCount() >= ShareLogic.MAX_PENDING) throw new InboxException(InboxException.Code.QUEUE_FULL);
            try {
                JSONArray list = new JSONArray();
                for (StoredFile file : files) {
                    list.put(new JSONObject()
                        .put("name", file.name)
                        .put("mime", file.mime)
                        .put("size", file.size)
                        .put("storedName", file.storedName));
                }
                JSONObject manifest = new JSONObject()
                    .put("id", id)
                    .put("createdAt", System.currentTimeMillis() / 1000.0)
                    .put("title", cleanTitle)
                    .put("text", cleanText)
                    .put("url", cleanUrl)
                    .put("files", list);
                byte[] bytes = manifest.toString().getBytes(StandardCharsets.UTF_8);
                // Duplicated fields count here: never write what read() refuses.
                if (bytes.length > MAX_MANIFEST_BYTES) throw new InboxException(InboxException.Code.TEXT_TOO_LARGE);
                try (FileOutputStream out = new FileOutputStream(new File(staging, MANIFEST))) {
                    out.write(bytes);
                    out.getFD().sync();
                }
            } catch (JSONException e) {
                throw new InboxException(InboxException.Code.CORRUPT, e);
            }
            if (!staging.renameTo(new File(root, id))) throw new IOException("cannot commit share");
            finished = true;
        }

        void discard() {
            if (finished) return;
            finished = true;
            deleteTree(staging);
        }
    }

    private void ensureRoot() throws IOException {
        if (!root.isDirectory() && !root.mkdirs()) throw new IOException("cannot create share inbox");
    }

    private List<File> committed() throws IOException {
        ensureRoot();
        File[] children = root.listFiles();
        if (children == null) throw new IOException("cannot list share inbox");
        List<File> out = new ArrayList<>();
        for (File child : children) {
            if (child.isDirectory() && isId(child.getName())) out.add(child);
        }
        return out;
    }

    private void sweepStaleStaging(long now) {
        File[] children = root.listFiles();
        if (children == null) return;
        for (File child : children) {
            String name = child.getName();
            if (name.startsWith(".") && name.endsWith(".tmp") && now - child.lastModified() > STALE_STAGING_MILLIS) {
                deleteTree(child);
            }
        }
    }

    private static Envelope read(File directory) throws IOException, InboxException {
        try {
            JSONObject manifest = new JSONObject(readText(new File(directory, MANIFEST)));
            String id = manifest.getString("id");
            if (!id.equals(directory.getName())) throw new InboxException(InboxException.Code.CORRUPT);
            JSONArray rows = manifest.getJSONArray("files");
            List<StoredFile> files = new ArrayList<>();
            for (int i = 0; i < rows.length(); i++) {
                JSONObject row = rows.getJSONObject(i);
                files.add(new StoredFile(row.getString("name"), row.getString("mime"), row.getLong("size"), row.getString("storedName")));
            }
            return new Envelope(id, manifest.getDouble("createdAt"), manifest.optString("title", ""),
                manifest.optString("text", ""), manifest.optString("url", ""), Collections.unmodifiableList(files), directory);
        } catch (JSONException e) {
            throw new InboxException(InboxException.Code.CORRUPT, e);
        }
    }

    private static String readText(File file) throws IOException {
        long length = file.length();
        if (length > MAX_MANIFEST_BYTES) throw new IOException("manifest too large");
        byte[] data = new byte[(int) length];
        try (InputStream in = new FileInputStream(file)) {
            int offset = 0;
            while (offset < data.length) {
                int n = in.read(data, offset, data.length - offset);
                if (n < 0) throw new IOException("short manifest");
                offset += n;
            }
        }
        return new String(data, StandardCharsets.UTF_8);
    }

    static void deleteTree(File file) {
        File[] children = file.listFiles();
        if (children != null) {
            for (File child : children) deleteTree(child);
        }
        file.delete();
    }
}

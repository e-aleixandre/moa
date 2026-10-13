package com.ealeixandre.moa.app;

import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collections;
import java.util.List;
import org.json.JSONObject;

// The /send JSON body, {"text":...,"attachments":[{"name","mime","data"}]},
// streamed from the staged files with its exact length known up front. Port of
// iOS ShareBodyWriter.swift: 32 MB of files is ~43 MB of base64, which is
// never built as one string. The files' sizes are fixed when they are staged;
// a file that changed since is an error, never a silently different body.
final class ShareBody implements NativeHttp.Body {

    static final class Part {
        final String name;
        final String mime;
        final File file;
        final long size;

        Part(String name, String mime, File file, long size) {
            this.name = name;
            this.mime = mime;
            this.file = file;
            this.size = size;
        }
    }

    // A multiple of 3, so each chunk's base64 concatenates without padding.
    static final int CHUNK = 3 * 64 * 1024;

    private final String text;
    private final List<Part> parts;
    private final int chunk;

    ShareBody(String text, List<Part> parts) {
        this(text, parts, CHUNK);
    }

    ShareBody(String text, List<Part> parts, int chunk) {
        if (chunk <= 0 || chunk % 3 != 0) throw new IllegalArgumentException("chunk");
        this.text = text;
        this.parts = Collections.unmodifiableList(new ArrayList<>(parts));
        this.chunk = chunk;
    }

    @Override
    public long length() {
        long total = utf8(head());
        for (int i = 0; i < parts.size(); i++) {
            Part part = parts.get(i);
            total += utf8(partHead(i, part)) + base64Length(part.size) + utf8(PART_TAIL);
        }
        return total + utf8(TAIL);
    }

    @Override
    public void writeTo(OutputStream out) throws IOException {
        out.write(head().getBytes(StandardCharsets.UTF_8));
        Base64.Encoder encoder = Base64.getEncoder();
        byte[] buffer = new byte[chunk];
        for (int i = 0; i < parts.size(); i++) {
            Part part = parts.get(i);
            out.write(partHead(i, part).getBytes(StandardCharsets.UTF_8));
            long read = 0;
            try (InputStream in = new FileInputStream(part.file)) {
                while (true) {
                    int filled = readFull(in, buffer);
                    if (filled == 0) break;
                    read += filled;
                    if (read > part.size) throw new IOException("shared file changed");
                    byte[] slice = filled == buffer.length ? buffer : java.util.Arrays.copyOf(buffer, filled);
                    out.write(encoder.encode(slice));
                    if (filled < buffer.length) break;
                }
            }
            if (read != part.size) throw new IOException("shared file changed");
            out.write(PART_TAIL.getBytes(StandardCharsets.UTF_8));
        }
        out.write(TAIL.getBytes(StandardCharsets.UTF_8));
    }

    private static final String PART_TAIL = "\"}";
    private static final String TAIL = "]}";

    private String head() {
        return "{\"text\":" + JSONObject.quote(text) + ",\"attachments\":[";
    }

    private static String partHead(int index, Part part) {
        return (index == 0 ? "" : ",") + "{\"name\":" + JSONObject.quote(part.name)
            + ",\"mime\":" + JSONObject.quote(part.mime) + ",\"data\":\"";
    }

    static long base64Length(long size) {
        return 4 * ((size + 2) / 3);
    }

    private static long utf8(String value) {
        return value.getBytes(StandardCharsets.UTF_8).length;
    }

    private static int readFull(InputStream in, byte[] buffer) throws IOException {
        int filled = 0;
        while (filled < buffer.length) {
            int n = in.read(buffer, filled, buffer.length - filled);
            if (n < 0) break;
            filled += n;
        }
        return filled;
    }
}

package com.ealeixandre.moa.app;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.fail;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collections;
import java.util.List;
import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.Rule;
import org.junit.Test;
import org.junit.rules.TemporaryFolder;

public class ShareBodyTest {

    @Rule
    public TemporaryFolder folder = new TemporaryFolder();

    private ShareBody.Part part(String name, String mime, byte[] content) throws IOException {
        File file = folder.newFile();
        Files.write(file.toPath(), content);
        return new ShareBody.Part(name, mime, file, content.length);
    }

    private static byte[] bytes(int size) {
        byte[] data = new byte[size];
        for (int i = 0; i < size; i++) data[i] = (byte) (i * 31 + 7);
        return data;
    }

    private static String write(ShareBody body) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        body.writeTo(out);
        assertEquals("declared length", body.length(), out.size());
        return new String(out.toByteArray(), StandardCharsets.UTF_8);
    }

    @Test
    public void streamsTheSendBodyWithItsExactLength() throws Exception {
        // Sizes around every base64 padding case, and chunks smaller than the
        // files so several chunks concatenate.
        int[] sizes = {0, 1, 2, 3, 4, 5, 10, 1000};
        for (int chunk : new int[] {3, 6, ShareBody.CHUNK}) {
            List<ShareBody.Part> parts = new ArrayList<>();
            for (int size : sizes) parts.add(part("f" + size + ".bin", "application/octet-stream", bytes(size)));
            String json = write(new ShareBody("léelo \"quoted\"\nhttps://a.example/x", parts, chunk));
            JSONObject body = new JSONObject(json);
            assertEquals("léelo \"quoted\"\nhttps://a.example/x", body.getString("text"));
            JSONArray attachments = body.getJSONArray("attachments");
            assertEquals(sizes.length, attachments.length());
            for (int i = 0; i < sizes.length; i++) {
                JSONObject attachment = attachments.getJSONObject(i);
                assertEquals("f" + sizes[i] + ".bin", attachment.getString("name"));
                assertEquals("application/octet-stream", attachment.getString("mime"));
                assertArrayEquals(bytes(sizes[i]), Base64.getDecoder().decode(attachment.getString("data")));
            }
        }
    }

    @Test
    public void textOnlyBody() throws Exception {
        assertEquals("{\"text\":\"hi\",\"attachments\":[]}", write(new ShareBody("hi", Collections.emptyList())));
    }

    @Test
    public void namesAreJsonEscaped() throws Exception {
        String json = write(new ShareBody("", Collections.singletonList(part("a\"b\\c ñ.png", "image/png", bytes(4)))));
        assertEquals("a\"b\\c ñ.png", new JSONObject(json).getJSONArray("attachments").getJSONObject(0).getString("name"));
    }

    @Test
    public void aFileThatChangedSinceStagingIsAnError() throws Exception {
        ShareBody.Part part = part("a.bin", "application/octet-stream", bytes(10));
        try (FileOutputStream out = new FileOutputStream(part.file, true)) {
            out.write(1);
        }
        try {
            new ShareBody("", Collections.singletonList(part)).writeTo(new ByteArrayOutputStream());
            fail("grown file");
        } catch (IOException expected) {
            // ok
        }
        Files.write(part.file.toPath(), bytes(9));
        try {
            new ShareBody("", Collections.singletonList(part)).writeTo(new ByteArrayOutputStream());
            fail("shrunk file");
        } catch (IOException expected) {
            // ok
        }
    }

    @Test
    public void theChunkMustKeepBase64Concatenable() {
        try {
            new ShareBody("", Collections.emptyList(), 4);
            fail();
        } catch (IllegalArgumentException expected) {
            // ok
        }
    }
}

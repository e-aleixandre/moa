package com.ealeixandre.moa.app;

import android.content.Context;
import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.util.Base64;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.json.JSONObject;

// window.MoaShareInbox: hands shares saved for later to moa's own picker
// (frontend data/native-share.js). Port of iOS ShareInboxMessageHandler. The
// local pairing page may only count them (status) and bind/clear the server;
// only the paired origin may read (peek) and remove (acknowledge) them, which
// OriginPolicy enforces before handle() runs. On Android the bound server is
// the credential's paired origin, so bindServer only checks it matches and
// clearServer has nothing to clear: unpairing already closes peek.
final class ShareInboxBridge implements WebBridges.MethodHandler {

    private final Context context;
    private final DeviceCredentialStore credentials;
    private final ExecutorService worker = Executors.newSingleThreadExecutor();

    ShareInboxBridge(Context context, DeviceCredentialStore credentials) {
        this.context = context.getApplicationContext();
        this.credentials = credentials;
    }

    @Override
    public void handle(String method, JSONObject options, OriginPolicy.Caller caller, WebBridges.Reply reply) {
        worker.execute(() -> {
            ShareInbox inbox = ShareInbox.in(context);
            try {
                switch (method) {
                    case "status":
                        reply.ok(new JSONObject().put("pending", inbox.pendingCount()));
                        return;
                    case "bindServer": {
                        String origin = OriginPolicy.normalizeOrigin(options.optString("origin", null));
                        String paired = OriginPolicy.normalizeOrigin(credentials.pairedOrigin());
                        if (origin == null || !origin.equals(paired)) {
                            reply.fail("invalid_origin", "The paired server address is invalid.");
                            return;
                        }
                        reply.ok(null);
                        return;
                    }
                    case "clearServer":
                        reply.ok(null);
                        return;
                    case "peek":
                        reply.okRaw(peek(inbox));
                        return;
                    case "acknowledge":
                        inbox.acknowledge(options.optString("id", null));
                        reply.ok(null);
                        return;
                    default:
                        reply.fail("unsupported", "The native share request is not supported.");
                }
            } catch (ShareInbox.InboxException e) {
                reply.fail("corrupt", "A pending share could not be read.");
            } catch (OutOfMemoryError e) {
                reply.fail("too_large", "The pending share is too large to open.");
            } catch (Exception e) {
                reply.fail("unavailable", "A pending share could not be read.");
            }
        });
    }

    // {"share": null} or {"share": {id,title,text,url,files:[{name,mime,size,data}]}},
    // the shape native-share.js decodes. Built as text so the base64 of the
    // files exists once, not once per JSON layer.
    private static String peek(ShareInbox inbox) throws Exception {
        ShareInbox.Envelope envelope = inbox.next();
        if (envelope == null) return "{\"share\":null}";
        long total = 0;
        for (ShareInbox.StoredFile file : envelope.files) {
            if (!ShareInbox.isId(file.storedName)) throw new ShareInbox.InboxException(ShareInbox.InboxException.Code.CORRUPT);
            total += file.size;
        }
        if (envelope.files.size() > ShareLogic.MAX_FILES || total > ShareLogic.MAX_BYTES) {
            throw new ShareInbox.InboxException(ShareInbox.InboxException.Code.CORRUPT);
        }
        StringBuilder out = new StringBuilder((int) Math.min(Integer.MAX_VALUE, ShareBody.base64Length(total) + 4096));
        out.append("{\"share\":{\"id\":").append(JSONObject.quote(envelope.id))
            .append(",\"title\":").append(JSONObject.quote(envelope.title))
            .append(",\"text\":").append(JSONObject.quote(envelope.text))
            .append(",\"url\":").append(JSONObject.quote(envelope.url))
            .append(",\"files\":[");
        Base64.Encoder encoder = Base64.getEncoder();
        for (int i = 0; i < envelope.files.size(); i++) {
            ShareInbox.StoredFile file = envelope.files.get(i);
            if (i > 0) out.append(',');
            out.append("{\"name\":").append(JSONObject.quote(file.name))
                .append(",\"mime\":").append(JSONObject.quote(file.mime))
                .append(",\"size\":").append(file.size)
                .append(",\"data\":\"");
            long read = 0;
            byte[] buffer = new byte[ShareBody.CHUNK];
            try (InputStream in = new FileInputStream(new File(envelope.directory, file.storedName))) {
                int n;
                while ((n = readFull(in, buffer)) > 0) {
                    read += n;
                    if (read > file.size) throw new ShareInbox.InboxException(ShareInbox.InboxException.Code.CORRUPT);
                    byte[] slice = n == buffer.length ? buffer : java.util.Arrays.copyOf(buffer, n);
                    out.append(new String(encoder.encode(slice), java.nio.charset.StandardCharsets.US_ASCII));
                    if (n < buffer.length) break;
                }
            }
            if (read != file.size) throw new ShareInbox.InboxException(ShareInbox.InboxException.Code.CORRUPT);
            out.append("\"}");
        }
        return out.append("]}}").toString();
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

package com.ealeixandre.moa.app;

import java.io.IOException;
import java.util.concurrent.Executor;
import java.util.concurrent.TimeUnit;
import okhttp3.Call;
import okhttp3.Callback;
import okhttp3.MediaType;
import okhttp3.OkHttpClient;
import okhttp3.Request;
import okhttp3.RequestBody;
import okhttp3.Response;
import org.json.JSONException;
import org.json.JSONObject;

// The invitation's HTTP: the long poll while it rings and the POSTs that
// answer, decline and hang up, all with the Moa-Voice key (iOS
// NativeIncomingCalls.follow and post). Like NativeHttp it follows no
// redirect and keeps no cookies, so the key never travels anywhere else.
// Replies are delivered on `deliver` (the call thread): status 0 means the
// server could not be reached, the body is null unless it is a JSON object.
final class InviteApi {

    private static final MediaType JSON = MediaType.get("application/json");
    // The server holds a poll up to 25 s; iOS gives it 30.
    private static final int POLL_SECONDS = 30;
    private static final int POST_SECONDS = 15;

    static OkHttpClient newClient() {
        return new OkHttpClient.Builder()
            .followRedirects(false)
            .followSslRedirects(false)
            .retryOnConnectionFailure(false)
            .connectTimeout(POST_SECONDS, TimeUnit.SECONDS)
            .build();
    }

    private final OkHttpClient client;
    private final Executor deliver;

    InviteApi(OkHttpClient client, Executor deliver) {
        this.client = client;
        this.deliver = deliver;
    }

    // origin is the paired server's: https, which the host guarantees.
    void poll(String origin, String authorization, String invite, IncomingCalls.Reply reply) {
        Request request = new Request.Builder()
            .url(origin + "/api/voice/invites/" + invite + "?wait=ringing")
            .header("Authorization", authorization)
            .build();
        enqueue(client.newBuilder().readTimeout(POLL_SECONDS, TimeUnit.SECONDS).build(), request, reply);
    }

    // path is under /api/voice/, e.g. "invites/<id>/answer".
    void post(String origin, String authorization, String path, IncomingCalls.Reply reply) {
        Request request = new Request.Builder()
            .url(origin + "/api/voice/" + path)
            .header("Authorization", authorization)
            .header("X-Moa-Request", "1")
            .post(RequestBody.create("{}", JSON))
            .build();
        enqueue(client.newBuilder().readTimeout(POST_SECONDS, TimeUnit.SECONDS).build(), request, reply);
    }

    private void enqueue(OkHttpClient http, Request request, IncomingCalls.Reply reply) {
        http.newCall(request).enqueue(new Callback() {
            @Override
            public void onFailure(Call call, IOException e) {
                deliver.execute(() -> reply.reply(0, null));
            }

            @Override
            public void onResponse(Call call, Response response) {
                int status = response.code();
                JSONObject body = null;
                try (Response r = response) {
                    String text = r.body() == null ? "" : r.body().string();
                    if (text.length() <= 64 * 1024) body = new JSONObject(text);
                } catch (IOException | JSONException e) {
                    // Not a JSON answer: only the status counts.
                }
                JSONObject parsed = body;
                deliver.execute(() -> reply.reply(status, parsed));
            }
        });
    }
}

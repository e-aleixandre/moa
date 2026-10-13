package com.ealeixandre.moa.app;

import android.webkit.PermissionRequest;
import com.getcapacitor.Bridge;
import com.getcapacitor.BridgeWebChromeClient;
import java.util.Arrays;
import java.util.List;

// Capacitor's chrome client already asks for RECORD_AUDIO/CAMERA at runtime
// before granting getUserMedia, but it grants any origin and any resource.
// moa only records from its own pages: the paired origin and the local page,
// and only microphone or camera capture. Everything else is denied.
final class MoaWebChromeClient extends BridgeWebChromeClient {

    private static final List<String> CAPTURE = Arrays.asList(
        PermissionRequest.RESOURCE_AUDIO_CAPTURE,
        PermissionRequest.RESOURCE_VIDEO_CAPTURE
    );

    private final DeviceCredentialStore credentials;

    MoaWebChromeClient(Bridge bridge, DeviceCredentialStore credentials) {
        super(bridge);
        this.credentials = credentials;
    }

    @Override
    public void onPermissionRequest(PermissionRequest request) {
        String origin = request.getOrigin() == null ? null : request.getOrigin().toString();
        if (OriginPolicy.classify(origin, credentials.pairedOrigin()) == OriginPolicy.Caller.OTHER
            || !CAPTURE.containsAll(Arrays.asList(request.getResources()))) {
            request.deny();
            return;
        }
        super.onPermissionRequest(request);
    }
}

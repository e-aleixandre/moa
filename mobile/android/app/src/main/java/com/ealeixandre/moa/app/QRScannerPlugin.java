package com.ealeixandre.moa.app;

import android.Manifest;
import android.content.pm.PackageManager;
import androidx.activity.result.ActivityResult;
import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import com.getcapacitor.PermissionState;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.ActivityCallback;
import com.getcapacitor.annotation.CapacitorPlugin;
import com.getcapacitor.annotation.Permission;
import com.journeyapps.barcodescanner.ScanIntentResult;
import com.journeyapps.barcodescanner.ScanOptions;

// The pairing screen's QR scanner, decoded on the device by ZXing: no Google
// Play services, ML Kit or Firebase involved. The Android side of iOS
// QRScannerPlugin.swift: the same plugin name and the four calls the local
// pairing page makes (www/barcode-scanner.js). scan resolves with
// `{ barcodes: [{ rawValue }] }`, an empty list when the person goes back.
// checkPermissions and requestPermissions are Capacitor's, for the "camera"
// alias below.
@CapacitorPlugin(
    name = "BarcodeScanner",
    permissions = { @Permission(alias = QRScannerPlugin.CAMERA, strings = { Manifest.permission.CAMERA }) }
)
public class QRScannerPlugin extends Plugin {

    static final String CAMERA = "camera";

    @PluginMethod
    public void isSupported(PluginCall call) {
        boolean camera = getContext().getPackageManager().hasSystemFeature(PackageManager.FEATURE_CAMERA_ANY);
        call.resolve(new JSObject().put("supported", camera));
    }

    @PluginMethod
    public void scan(PluginCall call) {
        if (getPermissionState(CAMERA) != PermissionState.GRANTED) {
            call.reject("Camera permission was not granted.");
            return;
        }
        ScanOptions options = new ScanOptions()
            .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            .setPrompt(getContext().getString(R.string.scanner_prompt))
            .setBeepEnabled(false)
            .setOrientationLocked(false);
        startActivityForResult(call, options.createScanIntent(getContext()), "scanResult");
    }

    @ActivityCallback
    private void scanResult(PluginCall call, ActivityResult result) {
        if (call == null) return;
        ScanIntentResult scanned = ScanIntentResult.parseActivityResult(result.getResultCode(), result.getData());
        String value = scanned.getContents();
        JSArray barcodes = new JSArray();
        if (value != null && !value.isEmpty()) {
            barcodes.put(new JSObject().put("rawValue", value).put("displayValue", value).put("format", "QR_CODE"));
        }
        call.resolve(new JSObject().put("barcodes", barcodes));
    }
}

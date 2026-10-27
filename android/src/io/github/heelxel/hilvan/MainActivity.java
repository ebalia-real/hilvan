package io.github.heelxel.hilvan;

import android.Manifest;
import android.annotation.SuppressLint;
import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.view.View;
import android.view.WindowInsets;
import android.webkit.MimeTypeMap;
import android.webkit.PermissionRequest;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;

import java.lang.ref.WeakReference;
import java.util.ArrayList;
import java.util.List;
import java.util.function.Consumer;

/** La ventana de Hilvan: la misma interfaz que en la computadora, adaptada al teléfono. */
public class MainActivity extends Activity {
    private static WeakReference<MainActivity> current = new WeakReference<>(null);
    private static final int REQ_FILES = 1, REQ_SAVE = 2, REQ_PERMS = 3, REQ_NOTIF = 4;

    private WebView web;
    private String base = "";
    private ValueCallback<Uri[]> fileCallback;
    private Consumer<Uri> saveCallback;
    private PermissionRequest pendingPermission;

    static MainActivity current() { return current.get(); }

    static void finishAll() {
        MainActivity a = current.get();
        if (a != null) a.runOnUiThread(a::finishAndRemoveTask);
    }

    @SuppressLint("SetJavaScriptEnabled")
    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);
        current = new WeakReference<>(this);
        if ((getApplicationInfo().flags & android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE) != 0) {
            WebView.setWebContentsDebuggingEnabled(true); // solo en compilaciones de pruebas
        }
        web = new WebView(this);
        web.setBackgroundColor(Color.parseColor("#141519"));
        // El WebView no respeta márgenes propios: va dentro de un contenedor que deja espacio para
        // la barra de estado, la de navegación y el teclado (Android 15 dibuja de borde a borde).
        FrameLayout root = new FrameLayout(this);
        root.setBackgroundColor(Color.parseColor("#141519"));
        root.addView(web, new FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT));
        setContentView(root);
        root.setOnApplyWindowInsetsListener((v, insets) -> {
            // Barras del sistema y recorte de la cámara (notch), lo que sea más alto; abajo, también el teclado.
            android.graphics.Insets bars = insets.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.displayCutout());
            int bottom = Math.max(bars.bottom, insets.getInsets(WindowInsets.Type.ime()).bottom);
            v.setPadding(bars.left, bars.top, bars.right, bottom);
            return WindowInsets.CONSUMED;
        });

        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setMediaPlaybackRequiresUserGesture(false);
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(true);
        s.setSupportMultipleWindows(false);
        s.setTextZoom(100);
        s.setUserAgentString(s.getUserAgentString() + " Hilvan/Android");

        web.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest req) {
                String u = req.getUrl().toString();
                if (!base.isEmpty() && u.startsWith(base)) return false;
                Files.openUrl(MainActivity.this, u);
                return true;
            }
        });
        web.setWebChromeClient(new WebChromeClient() {
            @Override
            public void onPermissionRequest(PermissionRequest request) {
                runOnUiThread(() -> askMediaPermissions(request));
            }

            @Override
            public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> cb, FileChooserParams p) {
                if (fileCallback != null) fileCallback.onReceiveValue(null);
                fileCallback = cb;
                Intent i = new Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE);
                String[] types = p.getAcceptTypes();
                String type = types != null && types.length > 0 && !types[0].isEmpty() ? types[0] : "*/*";
                if (type.startsWith(".")) {
                    String m = MimeTypeMap.getSingleton().getMimeTypeFromExtension(type.substring(1));
                    type = m != null ? m : "*/*";
                }
                if (type.contains(",")) {
                    i.putExtra(Intent.EXTRA_MIME_TYPES, type.split(","));
                    type = "*/*";
                }
                i.setType(type);
                i.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, p.getMode() == FileChooserParams.MODE_OPEN_MULTIPLE);
                try {
                    startActivityForResult(i, REQ_FILES);
                } catch (Exception e) {
                    fileCallback = null;
                    return false;
                }
                return true;
            }
        });

        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, REQ_NOTIF);
        }
        if (Platform.prefs(this).getBoolean("background", true)) HilvanService.startIt(this);
        new Thread(() -> {
            String url = Core.ensureStarted(getApplicationContext());
            runOnUiThread(() -> {
                if (url == null || url.isEmpty()) return;
                Uri u = Uri.parse(url);
                base = u.getScheme() + "://" + u.getAuthority() + "/";
                web.loadUrl(url);
                handleIntent(getIntent());
            });
        }).start();
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        handleIntent(intent);
    }

    private void handleIntent(Intent i) {
        if (i == null || base.isEmpty()) return;
        String room = i.getStringExtra("room");
        if (room != null && !room.isEmpty()) {
            Core.openRoom(i.getStringExtra("account"), room);
            i.removeExtra("room");
        }
        Uri data = i.getData();
        if (data != null && Intent.ACTION_VIEW.equals(i.getAction())) {
            Core.openUri(data.toString());
            i.setData(null);
        }
    }

    // Llamadas y notas de voz: se piden los permisos de Android cuando la interfaz los necesita.
    private void askMediaPermissions(PermissionRequest request) {
        List<String> need = new ArrayList<>();
        for (String r : request.getResources()) {
            if (PermissionRequest.RESOURCE_AUDIO_CAPTURE.equals(r)) need.add(Manifest.permission.RECORD_AUDIO);
            if (PermissionRequest.RESOURCE_VIDEO_CAPTURE.equals(r)) need.add(Manifest.permission.CAMERA);
        }
        List<String> missing = new ArrayList<>();
        for (String p : need) if (checkSelfPermission(p) != PackageManager.PERMISSION_GRANTED) missing.add(p);
        if (missing.isEmpty()) {
            request.grant(request.getResources());
            return;
        }
        pendingPermission = request;
        requestPermissions(missing.toArray(new String[0]), REQ_PERMS);
    }

    @Override
    public void onRequestPermissionsResult(int code, String[] perms, int[] results) {
        if (code != REQ_PERMS || pendingPermission == null) return;
        boolean ok = true;
        for (int r : results) ok &= r == PackageManager.PERMISSION_GRANTED;
        if (ok) pendingPermission.grant(pendingPermission.getResources());
        else pendingPermission.deny();
        pendingPermission = null;
    }

    void createDocument(String name, Consumer<Uri> cb) {
        if (saveCallback != null) saveCallback.accept(null);
        saveCallback = cb;
        String ext = name.contains(".") ? name.substring(name.lastIndexOf('.') + 1).toLowerCase() : "";
        String mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext);
        Intent i = new Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE)
                .setType(mime != null ? mime : "application/octet-stream")
                .putExtra(Intent.EXTRA_TITLE, name);
        try {
            startActivityForResult(i, REQ_SAVE);
        } catch (Exception e) {
            saveCallback = null;
            cb.accept(null);
        }
    }

    @Override
    protected void onActivityResult(int code, int result, Intent data) {
        if (code == REQ_FILES && fileCallback != null) {
            Uri[] uris = null;
            if (result == RESULT_OK && data != null) {
                if (data.getClipData() != null) {
                    uris = new Uri[data.getClipData().getItemCount()];
                    for (int k = 0; k < uris.length; k++) uris[k] = data.getClipData().getItemAt(k).getUri();
                } else if (data.getData() != null) {
                    uris = new Uri[]{data.getData()};
                }
            }
            fileCallback.onReceiveValue(uris);
            fileCallback = null;
        } else if (code == REQ_SAVE && saveCallback != null) {
            saveCallback.accept(result == RESULT_OK && data != null ? data.getData() : null);
            saveCallback = null;
        }
    }

    // Atrás: primero cierra lo que esté abierto en la interfaz (visor, menú, sala); si no hay nada,
    // deja Hilvan en segundo plano (sigue conectado).
    @Override
    public void onBackPressed() {
        if (base.isEmpty()) { moveTaskToBack(true); return; }
        web.evaluateJavascript("window.hvBack ? window.hvBack() : false", r -> {
            if (!"true".equals(r)) moveTaskToBack(true);
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        current = new WeakReference<>(this);
        web.onResume();
    }

    @Override
    protected void onPause() {
        web.onPause();
        super.onPause();
    }

    @Override
    protected void onDestroy() {
        if (current.get() == this) current = new WeakReference<>(null);
        web.destroy();
        super.onDestroy();
    }
}

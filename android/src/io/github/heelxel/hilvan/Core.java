package io.github.heelxel.hilvan;

import android.content.Context;
import android.os.Build;
import android.os.Environment;
import android.util.Log;

import org.json.JSONObject;
import org.json.JSONTokener;

/** Motor de Hilvan (Go, libhilvan.so): el mismo de la versión de escritorio. */
public final class Core {
    static { System.loadLibrary("hilvan"); }

    private static native String start(String dataDir, String downloadsDir, String model);
    static native void stop();
    static native void openRoom(String account, String room);
    static native void openUri(String uri);

    private static String url;

    /** Arranca el motor (una sola vez) y devuelve la dirección local de la interfaz. */
    static synchronized String ensureStarted(Context ctx) {
        if (url != null && !url.isEmpty()) return url;
        String data = ctx.getFilesDir().getAbsolutePath();
        String dl = ctx.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS) != null
                ? ctx.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS).getAbsolutePath() : data;
        String model = Build.MANUFACTURER == null ? Build.MODEL
                : (Build.MODEL.toLowerCase().startsWith(Build.MANUFACTURER.toLowerCase()) ? Build.MODEL : Build.MANUFACTURER + " " + Build.MODEL);
        url = start(data, dl, model);
        return url;
    }

    /** Lo llama el motor desde cualquier hilo. Devuelve una respuesta en texto. */
    @SuppressWarnings("unused")
    static String onCall(String kind, String json) {
        try {
            Context ctx = HilvanApp.get();
            switch (kind) {
                case "log": {
                    Object v = new JSONTokener(json).nextValue();
                    Log.i("Hilvan", String.valueOf(v));
                    return "";
                }
                case "notify":
                    Notifications.show(ctx, new JSONObject(json));
                    return "";
                case "cancel": {
                    JSONObject o = new JSONObject(json);
                    Notifications.cancel(ctx, o.optString("account"), o.optString("room"));
                    return "";
                }
                case "openUrl":
                    Files.openUrl(ctx, String.valueOf(new JSONTokener(json).nextValue()));
                    return "";
                case "saveDialog":
                    return Files.pickSaveLocation(new JSONObject(json).optString("name", "archivo"));
                case "export": {
                    JSONObject o = new JSONObject(json);
                    return Files.export(ctx, o.getString("src"), o.getString("dest"), o.optString("name"), o.optString("mime"));
                }
                case "quit":
                    HilvanService.quit(ctx);
                    return "";
                case "launcherIcon":
                    return Platform.setLauncherIcon(ctx, String.valueOf(new JSONTokener(json).nextValue()));
                case "settings": {
                    JSONObject o = new JSONObject(json);
                    Platform.applySettings(ctx, o.optBoolean("background", true), o.optBoolean("boot", true));
                    return "";
                }
                case "installApk":
                    return Platform.installApk(ctx, String.valueOf(new JSONTokener(json).nextValue()));
                case "platform":
                    return Platform.action(ctx, String.valueOf(new JSONTokener(json).nextValue()));
                default:
                    return "";
            }
        } catch (Throwable t) {
            Log.e("Hilvan", "onCall " + kind, t);
            return "error:" + t.getMessage();
        }
    }

    private Core() {}
}

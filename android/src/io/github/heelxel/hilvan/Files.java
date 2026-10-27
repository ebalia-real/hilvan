package io.github.heelxel.hilvan;

import android.content.ContentResolver;
import android.content.ContentValues;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.provider.MediaStore;

import java.io.FileInputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;

/** "Guardar como", Descargas y enlaces externos con las herramientas de Android. */
final class Files {
    /** Abre el selector de Android para elegir dónde guardar; devuelve la URI o "" si se cancela. */
    static String pickSaveLocation(String name) throws InterruptedException {
        MainActivity act = MainActivity.current();
        if (act == null) return "";
        CountDownLatch done = new CountDownLatch(1);
        AtomicReference<String> out = new AtomicReference<>("");
        act.runOnUiThread(() -> act.createDocument(name, uri -> {
            out.set(uri == null ? "" : uri.toString());
            done.countDown();
        }));
        done.await(10, TimeUnit.MINUTES);
        return out.get();
    }

    /** Copia src a dest (una URI elegida, o "downloads:" → Descargas/Hilvan). */
    static String export(Context ctx, String src, String dest, String name, String mime) throws Exception {
        ContentResolver cr = ctx.getContentResolver();
        Uri target;
        String shown;
        if (dest.startsWith("downloads:")) {
            ContentValues v = new ContentValues();
            v.put(MediaStore.Downloads.DISPLAY_NAME, name);
            if (mime != null && !mime.isEmpty()) v.put(MediaStore.Downloads.MIME_TYPE, mime);
            v.put(MediaStore.Downloads.RELATIVE_PATH, "Download/Hilvan");
            v.put(MediaStore.Downloads.IS_PENDING, 1);
            target = cr.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, v);
            if (target == null) throw new Exception("no se pudo crear el archivo en Descargas");
            shown = "Descargas/Hilvan/" + name;
        } else {
            target = Uri.parse(dest);
            shown = name;
        }
        try (InputStream in = new FileInputStream(src); OutputStream out = cr.openOutputStream(target, "wt")) {
            if (out == null) throw new Exception("no se pudo escribir");
            byte[] buf = new byte[1 << 16];
            int n;
            while ((n = in.read(buf)) > 0) out.write(buf, 0, n);
        }
        if (dest.startsWith("downloads:")) {
            ContentValues v = new ContentValues();
            v.put(MediaStore.Downloads.IS_PENDING, 0);
            cr.update(target, v, null, null);
        }
        return shown;
    }

    static void openUrl(Context ctx, String url) {
        Intent i = new Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        try { ctx.startActivity(i); } catch (Exception ignored) { }
    }

    private Files() {}
}

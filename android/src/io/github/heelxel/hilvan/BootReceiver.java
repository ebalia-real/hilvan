package io.github.heelxel.hilvan;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

/** Al prender el teléfono (o actualizar la app), Hilvan vuelve a conectarse solo. */
public class BootReceiver extends BroadcastReceiver {
    @Override
    public void onReceive(Context ctx, Intent intent) {
        // Al actualizar la app se reconecta siempre; al prender el teléfono, solo si está activado.
        boolean replaced = Intent.ACTION_MY_PACKAGE_REPLACED.equals(intent.getAction());
        android.content.SharedPreferences p = Platform.prefs(ctx);
        if (!p.getBoolean("background", true)) return;
        if (replaced || p.getBoolean("boot", true)) HilvanService.startIt(ctx);
    }
}

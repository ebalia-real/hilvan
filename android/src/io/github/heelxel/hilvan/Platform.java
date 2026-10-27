package io.github.heelxel.hilvan;

import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.PowerManager;
import android.provider.Settings;

/** Cosas propias de Android que pide la interfaz: ícono del lanzador, segundo plano, batería. */
final class Platform {
    static SharedPreferences prefs(Context ctx) {
        return ctx.getSharedPreferences("hilvan", Context.MODE_PRIVATE);
    }

    /**
     * Android solo permite elegir entre íconos incluidos en la app: cada estilo es un acceso
     * (activity-alias) y se deja activo solo el elegido. "Tu color" o "Tu imagen" no cambian el
     * lanzador (quedan dentro de la app).
     */
    static String setLauncherIcon(Context ctx, String style) {
        PackageManager pm = ctx.getPackageManager();
        String pkg = ctx.getPackageName();
        ComponentName target = new ComponentName(pkg, pkg + ".Icono_" + (style == null || style.isEmpty() ? "classic" : style));
        try {
            pm.getActivityInfo(target, PackageManager.MATCH_DISABLED_COMPONENTS);
        } catch (PackageManager.NameNotFoundException e) {
            return "sin-cambio"; // estilo propio: el lanzador queda como estaba
        }
        // Primero se activa el nuevo y después se apagan los demás: nunca queda la app sin ícono.
        pm.setComponentEnabledSetting(target, PackageManager.COMPONENT_ENABLED_STATE_ENABLED, PackageManager.DONT_KILL_APP);
        try {
            for (android.content.pm.ActivityInfo ai : pm.getPackageInfo(pkg,
                    PackageManager.GET_ACTIVITIES | PackageManager.MATCH_DISABLED_COMPONENTS).activities) {
                if (ai.name.contains(".Icono_") && !ai.name.equals(target.getClassName())) {
                    pm.setComponentEnabledSetting(new ComponentName(pkg, ai.name),
                            PackageManager.COMPONENT_ENABLED_STATE_DISABLED, PackageManager.DONT_KILL_APP);
                }
            }
        } catch (PackageManager.NameNotFoundException ignored) { }
        return "ok";
    }

    /** Seguir conectado con la app cerrada (servicio) y conectarse al prender el teléfono. */
    static void applySettings(Context ctx, boolean background, boolean boot) {
        prefs(ctx).edit().putBoolean("background", background).putBoolean("boot", boot).apply();
        if (background) HilvanService.startIt(ctx);
        else ctx.stopService(new Intent(ctx, HilvanService.class));
    }

    static String action(Context ctx, String what) {
        PowerManager pm = ctx.getSystemService(PowerManager.class);
        switch (what) {
            case "batteryStatus":
                return pm.isIgnoringBatteryOptimizations(ctx.getPackageName()) ? "libre" : "restringida";
            case "battery": {
                Intent i = new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                        Uri.parse("package:" + ctx.getPackageName())).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                try { ctx.startActivity(i); } catch (Exception e) {
                    ctx.startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
                }
                return "ok";
            }
            case "notifications": {
                Intent i = new Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                        .putExtra(Settings.EXTRA_APP_PACKAGE, ctx.getPackageName()).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                ctx.startActivity(i);
                return "ok";
            }
            default:
                return "";
        }
    }

    /**
     * Instala una actualización (ya verificada por el motor) con el instalador de Android: el
     * sistema muestra su confirmación y, como está firmada con la misma clave, actualiza la app.
     */
    static String installApk(Context ctx, String path) throws Exception {
        if (!ctx.getPackageManager().canRequestPackageInstalls()) {
            // Primera vez: Android pide permitir que Hilvan instale sus propias actualizaciones.
            ctx.startActivity(new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                    Uri.parse("package:" + ctx.getPackageName())).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
            return "error:Permití que Hilvan instale actualizaciones y tocá Actualizar de nuevo.";
        }
        android.content.pm.PackageInstaller pi = ctx.getPackageManager().getPackageInstaller();
        android.content.pm.PackageInstaller.SessionParams params =
                new android.content.pm.PackageInstaller.SessionParams(android.content.pm.PackageInstaller.SessionParams.MODE_FULL_INSTALL);
        params.setAppPackageName(ctx.getPackageName());
        int id = pi.createSession(params);
        try (android.content.pm.PackageInstaller.Session s = pi.openSession(id)) {
            java.io.File f = new java.io.File(path);
            try (java.io.InputStream in = new java.io.FileInputStream(f);
                 java.io.OutputStream out = s.openWrite("hilvan.apk", 0, f.length())) {
                byte[] buf = new byte[1 << 16];
                int n;
                while ((n = in.read(buf)) > 0) out.write(buf, 0, n);
                s.fsync(out);
            }
            Intent cb = new Intent(ctx, UpdateReceiver.class);
            android.app.PendingIntent pend = android.app.PendingIntent.getBroadcast(ctx, id, cb,
                    android.app.PendingIntent.FLAG_MUTABLE | android.app.PendingIntent.FLAG_UPDATE_CURRENT);
            s.commit(pend.getIntentSender());
        }
        return "ok";
    }

    private Platform() {}
}

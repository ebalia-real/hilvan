package io.github.heelxel.hilvan;

import android.app.Notification;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;

/** Servicio en primer plano: mantiene a Hilvan conectado con la app cerrada (mensajes y llamadas). */
public class HilvanService extends Service {
    static final int ID = 1;

    static void startIt(Context ctx) {
        Intent i = new Intent(ctx, HilvanService.class);
        try {
            ctx.startForegroundService(i);
        } catch (Exception ignored) {
            // Android puede no permitirlo en segundo plano; la app sigue funcionando abierta.
        }
    }

    static void quit(Context ctx) {
        ctx.stopService(new Intent(ctx, HilvanService.class));
        Core.stop();
        MainActivity.finishAll();
        android.os.Process.killProcess(android.os.Process.myPid());
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        Intent open = new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        PendingIntent pi = PendingIntent.getActivity(this, 0, open, PendingIntent.FLAG_IMMUTABLE);
        Notification n = new Notification.Builder(this, Notifications.CH_SERVICE)
                .setSmallIcon(R.drawable.ic_stat)
                .setContentTitle("Hilvan está conectado")
                .setContentText("Recibís mensajes y llamadas con la app cerrada")
                .setContentIntent(pi)
                .setOngoing(true)
                .setShowWhen(false)
                .build();
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING);
        } else {
            startForeground(ID, n);
        }
        new Thread(() -> Core.ensureStarted(getApplicationContext())).start();
        return START_STICKY;
    }

    @Override
    public IBinder onBind(Intent intent) { return null; }
}

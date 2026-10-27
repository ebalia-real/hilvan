package io.github.heelxel.hilvan;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;

import org.json.JSONObject;

/** Notificaciones de mensajes, invitaciones y llamadas. */
final class Notifications {
    static final String CH_MESSAGES = "mensajes";
    static final String CH_CALLS = "llamadas";
    static final String CH_SERVICE = "servicio";

    static void createChannels(Context ctx) {
        NotificationManager nm = ctx.getSystemService(NotificationManager.class);
        NotificationChannel msg = new NotificationChannel(CH_MESSAGES, "Mensajes", NotificationManager.IMPORTANCE_HIGH);
        msg.setDescription("Mensajes nuevos, menciones e invitaciones");
        NotificationChannel calls = new NotificationChannel(CH_CALLS, "Llamadas", NotificationManager.IMPORTANCE_HIGH);
        calls.setDescription("Llamadas entrantes");
        NotificationChannel svc = new NotificationChannel(CH_SERVICE, "Conexión en segundo plano", NotificationManager.IMPORTANCE_MIN);
        svc.setDescription("Aviso fijo mientras Hilvan está conectado");
        svc.setShowBadge(false);
        nm.createNotificationChannel(msg);
        nm.createNotificationChannel(calls);
        nm.createNotificationChannel(svc);
    }

    private static int idFor(String account, String room) {
        int h = (account + "\n" + room).hashCode();
        return h == HilvanService.ID ? h + 1 : h;
    }

    static void show(Context ctx, JSONObject o) {
        String account = o.optString("account"), room = o.optString("room"), kind = o.optString("kind");
        boolean call = "call".equals(kind);
        Intent open = new Intent(ctx, MainActivity.class)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP)
                .putExtra("account", account).putExtra("room", room);
        PendingIntent pi = PendingIntent.getActivity(ctx, idFor(account, room), open,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        Notification.Builder b = new Notification.Builder(ctx, call ? CH_CALLS : CH_MESSAGES)
                .setSmallIcon(R.drawable.ic_stat)
                .setColor(0xFF6D5DFC)
                .setContentTitle(o.optString("title", "Hilvan"))
                .setContentText(o.optString("body"))
                .setStyle(new Notification.BigTextStyle().bigText(o.optString("body")))
                .setContentIntent(pi)
                .setAutoCancel(!call)
                .setGroup(room)
                .setCategory(call ? Notification.CATEGORY_CALL : Notification.CATEGORY_MESSAGE);
        if (call) b.setFullScreenIntent(pi, true).setOngoing(true).setTimeoutAfter(60_000);
        ctx.getSystemService(NotificationManager.class).notify(idFor(account, room), b.build());
    }

    static void cancel(Context ctx, String account, String room) {
        ctx.getSystemService(NotificationManager.class).cancel(idFor(account, room));
    }

    private Notifications() {}
}

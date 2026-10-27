package io.github.heelxel.hilvan;

import android.app.Application;
import android.content.Context;

public class HilvanApp extends Application {
    private static HilvanApp app;

    static Context get() { return app; }

    @Override
    public void onCreate() {
        super.onCreate();
        app = this;
        Notifications.createChannels(this);
    }
}

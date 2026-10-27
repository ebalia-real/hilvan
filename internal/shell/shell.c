// Ventana nativa de Hilvan: GTK4 + WebKitGTK 6.0.
#include <gtk/gtk.h>
#include <webkit/webkit.h>
#include <string.h>
#include <stdlib.h>
#include "shell.h"

extern void goShellReady(void);
extern void goShellClosed(int width, int height, int maximized);
extern void goShellOpenExternal(char *uri);
extern void goShellDownloadDone(char *path, int ok);
extern void goShellActivateURI(char *uri);
extern void goShellVisible(int visible);
extern void goShellSaveChosen(int id, char *path);

static GtkApplication *app = NULL;
static GtkWindow *win = NULL;
static WebKitWebView *view = NULL;
static char *home_uri = NULL;   // http://127.0.0.1:PUERTO
static char *start_uri = NULL;  // con token
static char *data_dir = NULL;
static int opt_w = 1200, opt_h = 800, opt_max = 0, opt_dev = 0;
static char *pending_uri = NULL;
static int run_in_background = 0, start_hidden = 0;
static void apply_color_scheme(void);
// Capa de video nativo (llamadas): GtkPicture sobre la WebView, sin pasar por el navegador.
static GtkWidget *video_layer = NULL;
static GHashTable *video_tiles = NULL; // stream → GtkPicture

// HILVAN_DEV_APP_ID permite abrir una copia de pruebas junto a la instalada.
static const char *app_id(void) {
    const char *e = g_getenv("HILVAN_DEV_APP_ID");
    return e && *e ? e : HILVAN_APP_ID;
}

static gboolean is_own_uri(const char *uri) {
    return uri && home_uri && g_str_has_prefix(uri, home_uri);
}

// --- permisos: cámara/micrófono/pantalla solo para nuestra propia interfaz ---
static gboolean on_permission(WebKitWebView *v, WebKitPermissionRequest *req, gpointer data) {
    const char *uri = webkit_web_view_get_uri(v);
    if (!is_own_uri(uri)) {
        webkit_permission_request_deny(req);
        return TRUE;
    }
    if (WEBKIT_IS_USER_MEDIA_PERMISSION_REQUEST(req) || WEBKIT_IS_DEVICE_INFO_PERMISSION_REQUEST(req) ||
        WEBKIT_IS_CLIPBOARD_PERMISSION_REQUEST(req)) {
        webkit_permission_request_allow(req);
        return TRUE;
    }
    // Notificaciones web: las maneja el núcleo por D-Bus; geolocalización: se permite (compartir ubicación).
    if (WEBKIT_IS_GEOLOCATION_PERMISSION_REQUEST(req)) {
        webkit_permission_request_allow(req);
        return TRUE;
    }
    webkit_permission_request_deny(req);
    return TRUE;
}

// --- navegación: todo lo externo se abre en el navegador del sistema ---
static gboolean on_decide_policy(WebKitWebView *v, WebKitPolicyDecision *decision, WebKitPolicyDecisionType type, gpointer data) {
    if (type == WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION || type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
        WebKitNavigationPolicyDecision *nd = WEBKIT_NAVIGATION_POLICY_DECISION(decision);
        WebKitNavigationAction *action = webkit_navigation_policy_decision_get_navigation_action(nd);
        WebKitURIRequest *req = webkit_navigation_action_get_request(action);
        const char *uri = webkit_uri_request_get_uri(req);
        if (type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION || !is_own_uri(uri)) {
            if (uri && (g_str_has_prefix(uri, "http://") || g_str_has_prefix(uri, "https://") || g_str_has_prefix(uri, "mailto:"))) {
                goShellOpenExternal((char *)uri);
            }
            webkit_policy_decision_ignore(decision);
            return TRUE;
        }
    }
    if (type == WEBKIT_POLICY_DECISION_TYPE_RESPONSE) {
        WebKitResponsePolicyDecision *rd = WEBKIT_RESPONSE_POLICY_DECISION(decision);
        if (!webkit_response_policy_decision_is_main_frame_main_resource(rd)) return FALSE;
        if (!webkit_response_policy_decision_is_mime_type_supported(rd)) {
            webkit_policy_decision_download(decision);
            return TRUE;
        }
    }
    return FALSE;
}

// --- descargas: a ~/Descargas sin diálogos ---
static char *unique_path(const char *dir, const char *name) {
    char *safe = g_path_get_basename(name && *name ? name : "archivo");
    char *path = g_build_filename(dir, safe, NULL);
    int n = 1;
    while (g_file_test(path, G_FILE_TEST_EXISTS) && n < 1000) {
        g_free(path);
        const char *dot = strrchr(safe, '.');
        char *alt;
        if (dot && dot != safe) {
            char *stem = g_strndup(safe, dot - safe);
            alt = g_strdup_printf("%s (%d)%s", stem, n, dot);
            g_free(stem);
        } else {
            alt = g_strdup_printf("%s (%d)", safe, n);
        }
        path = g_build_filename(dir, alt, NULL);
        g_free(alt);
        n++;
    }
    g_free(safe);
    return path;
}

static gboolean on_decide_destination(WebKitDownload *dl, const char *suggested, gpointer data) {
    const char *dir = g_get_user_special_dir(G_USER_DIRECTORY_DOWNLOAD);
    if (!dir) dir = g_get_home_dir();
    char *path = unique_path(dir, suggested);
    webkit_download_set_destination(dl, path);
    g_free(path);
    return TRUE;
}

static void on_download_finished(WebKitDownload *dl, gpointer data) {
    const char *dest = webkit_download_get_destination(dl);
    if (dest) goShellDownloadDone((char *)dest, 1);
}

static void on_download_failed(WebKitDownload *dl, GError *err, gpointer data) {
    const char *dest = webkit_download_get_destination(dl);
    goShellDownloadDone((char *)(dest ? dest : ""), 0);
}

static void on_download_started(WebKitNetworkSession *s, WebKitDownload *dl, gpointer data) {
    g_signal_connect(dl, "decide-destination", G_CALLBACK(on_decide_destination), NULL);
    g_signal_connect(dl, "finished", G_CALLBACK(on_download_finished), NULL);
    g_signal_connect(dl, "failed", G_CALLBACK(on_download_failed), NULL);
}

// --- menú contextual: sin "Recargar" ni navegación (salvo modo desarrollo) ---
static gboolean on_context_menu(WebKitWebView *v, WebKitContextMenu *menu, WebKitHitTestResult *hit, gpointer data) {
    if (opt_dev) return FALSE;
    GList *items = g_list_copy(webkit_context_menu_get_items(menu));
    for (GList *l = items; l; l = l->next) {
        WebKitContextMenuItem *it = l->data;
        WebKitContextMenuAction a = webkit_context_menu_item_get_stock_action(it);
        switch (a) {
        case WEBKIT_CONTEXT_MENU_ACTION_RELOAD:
        case WEBKIT_CONTEXT_MENU_ACTION_STOP:
        case WEBKIT_CONTEXT_MENU_ACTION_GO_BACK:
        case WEBKIT_CONTEXT_MENU_ACTION_GO_FORWARD:
        case WEBKIT_CONTEXT_MENU_ACTION_INSPECT_ELEMENT:
        case WEBKIT_CONTEXT_MENU_ACTION_OPEN_LINK_IN_NEW_WINDOW:
        case WEBKIT_CONTEXT_MENU_ACTION_OPEN_IMAGE_IN_NEW_WINDOW:
        case WEBKIT_CONTEXT_MENU_ACTION_OPEN_FRAME_IN_NEW_WINDOW:
        case WEBKIT_CONTEXT_MENU_ACTION_OPEN_VIDEO_IN_NEW_WINDOW:
        case WEBKIT_CONTEXT_MENU_ACTION_OPEN_AUDIO_IN_NEW_WINDOW:
        case WEBKIT_CONTEXT_MENU_ACTION_DOWNLOAD_LINK_TO_DISK:
        case WEBKIT_CONTEXT_MENU_ACTION_DOWNLOAD_IMAGE_TO_DISK:
        case WEBKIT_CONTEXT_MENU_ACTION_DOWNLOAD_VIDEO_TO_DISK:
        case WEBKIT_CONTEXT_MENU_ACTION_DOWNLOAD_AUDIO_TO_DISK:
            webkit_context_menu_remove(menu, it);
            break;
        default:
            break;
        }
    }
    g_list_free(items);
    // Quitar separadores sobrantes al principio/final.
    GList *rest = webkit_context_menu_get_items(menu);
    if (!rest) return TRUE; // menú vacío: no mostrar
    return FALSE;
}

// --- diagnóstico: captura de la vista (HILVAN_SNAPSHOT=/ruta.png) ---
static void snapshot_done(GObject *src, GAsyncResult *res, gpointer data) {
    char *path = data;
    GError *err = NULL;
    GdkTexture *tex = webkit_web_view_get_snapshot_finish(WEBKIT_WEB_VIEW(src), res, &err);
    if (tex) {
        gdk_texture_save_to_png(tex, path);
        g_object_unref(tex);
    } else if (err) {
        g_warning("snapshot: %s", err->message);
        g_error_free(err);
    }
    g_free(path);
}
static gboolean take_snapshot(gpointer data) {
    const char *path = g_getenv("HILVAN_SNAPSHOT");
    if (view && path && *path)
        webkit_web_view_get_snapshot(view, WEBKIT_SNAPSHOT_REGION_VISIBLE, WEBKIT_SNAPSHOT_OPTIONS_NONE, NULL, snapshot_done, g_strdup(path));
    return G_SOURCE_CONTINUE;
}

// Si el proceso web se cae (raro), recargamos sin perder la sesión.
static void on_web_process_terminated(WebKitWebView *v, WebKitWebProcessTerminationReason reason, gpointer data) {
    webkit_web_view_load_uri(v, home_uri);
}

static gboolean on_close_request(GtkWindow *w, gpointer data) {
    int width = gtk_widget_get_width(GTK_WIDGET(w));
    int height = gtk_widget_get_height(GTK_WIDGET(w));
    goShellClosed(width, height, gtk_window_is_maximized(w) ? 1 : 0);
    if (run_in_background) {
        // Seguimos sincronizando y notificando; el lanzador vuelve a mostrar la ventana.
        gtk_widget_set_visible(GTK_WIDGET(w), FALSE);
        return TRUE;
    }
    return FALSE;
}

// La bandeja muestra "Mostrar" u "Ocultar" según la ventana.
static void on_visible(GObject *o, GParamSpec *ps, gpointer data) {
    goShellVisible(gtk_widget_get_visible(GTK_WIDGET(o)) ? 1 : 0);
}

static void build_window(GtkApplication *a) {
    WebKitNetworkSession *session;
    if (data_dir) {
        char *d = g_build_filename(data_dir, "webkit", "data", NULL);
        char *c = g_build_filename(data_dir, "webkit", "cache", NULL);
        session = webkit_network_session_new(d, c);
        g_free(d);
        g_free(c);
    } else {
        session = webkit_network_session_new_ephemeral();
    }
    g_signal_connect(session, "download-started", G_CALLBACK(on_download_started), NULL);
    webkit_network_session_set_itp_enabled(session, TRUE);

    WebKitSettings *settings = webkit_settings_new();
    webkit_settings_set_enable_developer_extras(settings, opt_dev ? TRUE : FALSE);
    webkit_settings_set_enable_write_console_messages_to_stdout(settings, opt_dev ? TRUE : FALSE);
    webkit_settings_set_enable_media_stream(settings, TRUE);
    webkit_settings_set_enable_mediasource(settings, TRUE);
    webkit_settings_set_enable_media(settings, TRUE);
    webkit_settings_set_enable_encrypted_media(settings, FALSE);
    webkit_settings_set_enable_webrtc(settings, TRUE);
    webkit_settings_set_media_playback_requires_user_gesture(settings, FALSE);
    // Los videos se reproducen dentro de la app (como Telegram); pantalla completa solo si se pide.
    webkit_settings_set_media_playback_allows_inline(settings, TRUE);
    webkit_settings_set_enable_back_forward_navigation_gestures(settings, FALSE);
    webkit_settings_set_javascript_can_open_windows_automatically(settings, FALSE);
    webkit_settings_set_allow_file_access_from_file_urls(settings, FALSE);
    webkit_settings_set_allow_universal_access_from_file_urls(settings, FALSE);
    webkit_settings_set_enable_smooth_scrolling(settings, TRUE);
    webkit_settings_set_hardware_acceleration_policy(settings, WEBKIT_HARDWARE_ACCELERATION_POLICY_ALWAYS);
    webkit_settings_set_user_agent_with_application_details(settings, "Hilvan", NULL);

    view = WEBKIT_WEB_VIEW(g_object_new(WEBKIT_TYPE_WEB_VIEW,
        "network-session", session,
        "settings", settings,
        NULL));
    g_object_unref(settings);

    // Corrector ortográfico (idiomas del sistema).
    WebKitWebContext *ctx = webkit_web_view_get_context(view);
    webkit_web_context_set_spell_checking_enabled(ctx, TRUE);
    const char *const langs[] = {"es_ES", "es_AR", "en_US", NULL};
    webkit_web_context_set_spell_checking_languages(ctx, langs);

    GdkRGBA bg = {0.08, 0.09, 0.105, 1.0};
    webkit_web_view_set_background_color(view, &bg);

    g_signal_connect(view, "permission-request", G_CALLBACK(on_permission), NULL);
    g_signal_connect(view, "decide-policy", G_CALLBACK(on_decide_policy), NULL);
    g_signal_connect(view, "context-menu", G_CALLBACK(on_context_menu), NULL);
    g_signal_connect(view, "web-process-terminated", G_CALLBACK(on_web_process_terminated), NULL);

    apply_color_scheme();
    win = GTK_WINDOW(gtk_application_window_new(a));
    gtk_window_set_title(win, "Hilvan");
    gtk_window_set_icon_name(win, app_id());
    gtk_window_set_default_size(win, opt_w, opt_h);
    if (opt_max) gtk_window_maximize(win);
    GtkWidget *overlay = gtk_overlay_new();
    gtk_overlay_set_child(GTK_OVERLAY(overlay), GTK_WIDGET(view));
    video_layer = gtk_fixed_new();
    gtk_widget_set_can_target(video_layer, FALSE);
    gtk_widget_set_can_focus(video_layer, FALSE);
    gtk_overlay_add_overlay(GTK_OVERLAY(overlay), video_layer);
    gtk_overlay_set_clip_overlay(GTK_OVERLAY(overlay), video_layer, TRUE);
    video_tiles = g_hash_table_new_full(g_str_hash, g_str_equal, g_free, NULL);
    GtkCssProvider *css = gtk_css_provider_new();
    gtk_css_provider_load_from_string(css, "picture.hv-video { border-radius: 12px; background: #0b0c0f; }");
    gtk_style_context_add_provider_for_display(gdk_display_get_default(), GTK_STYLE_PROVIDER(css), GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
    g_object_unref(css);
    gtk_window_set_child(win, overlay);
    g_signal_connect(win, "close-request", G_CALLBACK(on_close_request), NULL);
    g_signal_connect(win, "notify::visible", G_CALLBACK(on_visible), NULL);

    webkit_web_view_load_uri(view, start_uri);
    if (!start_hidden) gtk_window_present(win);
    if (g_getenv("HILVAN_SNAPSHOT")) g_timeout_add_seconds(4, take_snapshot, NULL);
    goShellReady();
    if (pending_uri) {
        goShellActivateURI(pending_uri);
        g_free(pending_uri);
        pending_uri = NULL;
    }
}

static void on_activate(GtkApplication *a, gpointer data) {
    if (win) {
        gtk_window_present(win);
        return;
    }
    build_window(a);
}

static void on_open(GApplication *a, GFile **files, gint n, const char *hint, gpointer data) {
    for (int i = 0; i < n; i++) {
        char *uri = g_file_get_uri(files[i]);
        if (win) goShellActivateURI(uri);
        else { g_free(pending_uri); pending_uri = g_strdup(uri); }
        g_free(uri);
    }
    on_activate(GTK_APPLICATION(a), NULL);
}

// shell_init crea y registra la aplicación. Si ya hay una instancia corriendo, le pasa
// el control (y los enlaces matrix:) y devuelve 1 para que este proceso termine.
int shell_init(int argc, char **argv) {
    // El renderizador Vulkan de GTK falla al importar cuadros de video en algunos controladores
    // (p. ej. NVIDIA: VK_ERROR_OUT_OF_DEVICE_MEMORY y cierre). OpenGL es estable en todos.
    g_setenv("GSK_RENDERER", "gl", FALSE);
    g_set_prgname("hilvan");
    g_set_application_name("Hilvan");
    app = gtk_application_new(app_id(), G_APPLICATION_HANDLES_OPEN);
    g_signal_connect(app, "activate", G_CALLBACK(on_activate), NULL);
    g_signal_connect(app, "open", G_CALLBACK(on_open), NULL);
    GError *err = NULL;
    if (!g_application_register(G_APPLICATION(app), NULL, &err)) {
        if (err) g_error_free(err);
        return 0;
    }
    if (!g_application_get_is_remote(G_APPLICATION(app))) return 0;
    int nfiles = 0;
    for (int i = 1; i < argc; i++)
        if (g_str_has_prefix(argv[i], "matrix:") || g_str_has_prefix(argv[i], "https://matrix.to/")) nfiles++;
    if (nfiles > 0) {
        GFile **files = g_new0(GFile *, nfiles);
        int j = 0;
        for (int i = 1; i < argc; i++)
            if (g_str_has_prefix(argv[i], "matrix:") || g_str_has_prefix(argv[i], "https://matrix.to/")) files[j++] = g_file_new_for_uri(argv[i]);
        g_application_open(G_APPLICATION(app), files, nfiles, "");
        for (int i = 0; i < nfiles; i++) g_object_unref(files[i]);
        g_free(files);
    } else {
        g_application_activate(G_APPLICATION(app));
    }
    // El pedido a la instancia abierta sale por D-Bus en segundo plano: hay que esperar a que se
    // envíe antes de terminar este proceso, o se pierde (y el dock queda "cargando" para siempre).
    GDBusConnection *bus = g_application_get_dbus_connection(G_APPLICATION(app));
    if (bus) g_dbus_connection_flush_sync(bus, NULL, NULL);
    return 1;
}

int shell_run(const char *home, const char *start, const char *datadir, int w, int h, int maximized, int dev, const char *initial_uri, int hidden) {
    start_hidden = hidden;
    home_uri = g_strdup(home);
    start_uri = g_strdup(start);
    data_dir = datadir && *datadir ? g_strdup(datadir) : NULL;
    if (w > 300) opt_w = w;
    if (h > 300) opt_h = h;
    opt_max = maximized;
    opt_dev = dev;
    if (initial_uri && *initial_uri) pending_uri = g_strdup(initial_uri);
    char *argv[] = {"hilvan", NULL};
    int status = g_application_run(G_APPLICATION(app), 1, argv);
    g_object_unref(app);
    return status;
}

// --- llamadas seguras desde otros hilos ---
void shell_set_background(int on) { run_in_background = on; }

static gboolean idle_present(gpointer data) {
    if (win) gtk_window_present(win);
    return G_SOURCE_REMOVE;
}
void shell_present(void) { g_idle_add(idle_present, NULL); }

// --- "Guardar como": diálogo de archivos del sistema, encima de la ventana ---
typedef struct { int id; char *title, *name, *folder; } SaveReq;
static void save_done(GObject *src, GAsyncResult *res, gpointer data) {
    GFile *f = gtk_file_dialog_save_finish(GTK_FILE_DIALOG(src), res, NULL);
    char *path = f ? g_file_get_path(f) : NULL;
    goShellSaveChosen(GPOINTER_TO_INT(data), path ? path : "");
    g_free(path);
    if (f) g_object_unref(f);
    g_object_unref(src);
}
static gboolean idle_save(gpointer data) {
    SaveReq *r = data;
    GtkFileDialog *d = gtk_file_dialog_new();
    gtk_file_dialog_set_title(d, r->title);
    gtk_file_dialog_set_initial_name(d, r->name);
    gtk_file_dialog_set_modal(d, TRUE);
    if (r->folder && *r->folder) {
        GFile *f = g_file_new_for_path(r->folder);
        gtk_file_dialog_set_initial_folder(d, f);
        g_object_unref(f);
    }
    if (win && !gtk_widget_get_visible(GTK_WIDGET(win))) gtk_window_present(win);
    gtk_file_dialog_save(d, win, NULL, save_done, GINT_TO_POINTER(r->id));
    g_free(r->title); g_free(r->name); g_free(r->folder); g_free(r);
    return G_SOURCE_REMOVE;
}
void shell_save_dialog(int id, const char *title, const char *name, const char *folder) {
    SaveReq *r = g_new0(SaveReq, 1);
    r->id = id; r->title = g_strdup(title); r->name = g_strdup(name); r->folder = g_strdup(folder);
    g_idle_add(idle_save, r);
}

// --- bandeja del sistema ---
static void show_with_token(const char *token) {
    if (!win) return;
    // En Wayland, el token de activación de la bandeja deja pasar la ventana al frente.
    if (token && *token) gtk_window_set_startup_id(win, token);
    gtk_window_present(win);
}
static gboolean idle_show(gpointer data) {
    show_with_token(data);
    g_free(data);
    return G_SOURCE_REMOVE;
}
void shell_show(const char *token) { g_idle_add(idle_show, g_strdup(token ? token : "")); }

static gboolean idle_hide(gpointer data) {
    if (win && gtk_widget_get_visible(GTK_WIDGET(win))) {
        goShellClosed(gtk_widget_get_width(GTK_WIDGET(win)), gtk_widget_get_height(GTK_WIDGET(win)), gtk_window_is_maximized(win) ? 1 : 0);
        gtk_widget_set_visible(GTK_WIDGET(win), FALSE);
    }
    return G_SOURCE_REMOVE;
}
void shell_hide(void) { g_idle_add(idle_hide, NULL); }

// Clic en el ícono de la bandeja: si la ventana está al frente se oculta; si no, aparece.
static gboolean idle_toggle(gpointer data) {
    if (win && gtk_widget_get_visible(GTK_WIDGET(win)) && gtk_window_is_active(win)) idle_hide(NULL);
    else show_with_token(data);
    g_free(data);
    return G_SOURCE_REMOVE;
}
void shell_toggle(const char *token) { g_idle_add(idle_toggle, g_strdup(token ? token : "")); }

static gboolean idle_quit(gpointer data) {
    if (win) gtk_window_destroy(win);
    if (app) g_application_quit(G_APPLICATION(app));
    return G_SOURCE_REMOVE;
}
void shell_quit(void) { g_idle_add(idle_quit, NULL); }

static gboolean idle_eval(gpointer data) {
    char *js = data;
    if (view) webkit_web_view_evaluate_javascript(view, js, -1, NULL, NULL, NULL, NULL, NULL);
    g_free(js);
    return G_SOURCE_REMOVE;
}
void shell_eval(const char *js) { g_idle_add(idle_eval, g_strdup(js)); }

static void launch_done(GObject *src, GAsyncResult *res, gpointer data) {
    GError *err = NULL;
    if (!gtk_uri_launcher_launch_finish(GTK_URI_LAUNCHER(src), res, &err) && err) {
        g_warning("No se pudo abrir el enlace: %s", err->message);
        g_error_free(err);
    }
    g_object_unref(src);
}
static gboolean idle_open(gpointer data) {
    char *uri = data;
    GtkUriLauncher *l = gtk_uri_launcher_new(uri);
    gtk_uri_launcher_launch(l, win, NULL, launch_done, NULL);
    g_free(uri);
    return G_SOURCE_REMOVE;
}
void shell_open_uri(const char *uri) { g_idle_add(idle_open, g_strdup(uri)); }

// --- tema: barra de título clara u oscura según la app ---
static int want_dark = -1; // -1 = seguir al sistema
static void apply_color_scheme(void) {
    GtkSettings *gs = gtk_settings_get_default();
    if (!gs) return;
    GObjectClass *klass = G_OBJECT_GET_CLASS(gs);
    if (g_object_class_find_property(klass, "gtk-interface-color-scheme")) {
        // GTK >= 4.20: 0 = predeterminado (sistema), 2 = claro, 3 = oscuro.
        int scheme = want_dark < 0 ? 1 : (want_dark ? 3 : 2);
        GParamSpec *ps = g_object_class_find_property(klass, "gtk-interface-color-scheme");
        GEnumClass *ec = g_type_class_ref(ps->value_type);
        const char *nick = want_dark < 0 ? "default" : (want_dark ? "dark" : "light");
        GEnumValue *ev = g_enum_get_value_by_nick(ec, nick);
        if (ev) scheme = ev->value;
        g_type_class_unref(ec);
        g_object_set(gs, "gtk-interface-color-scheme", scheme, NULL);
    }
    if (g_object_class_find_property(klass, "gtk-application-prefer-dark-theme"))
        g_object_set(gs, "gtk-application-prefer-dark-theme", want_dark == 1, NULL);
    if (view) {
        GdkRGBA dark = {0.08, 0.09, 0.105, 1.0}, light = {1, 1, 1, 1};
        webkit_web_view_set_background_color(view, want_dark == 0 ? &light : &dark);
    }
}
static gboolean idle_dark(gpointer data) {
    want_dark = GPOINTER_TO_INT(data);
    apply_color_scheme();
    return G_SOURCE_REMOVE;
}
void shell_set_dark(int dark) { g_idle_add(idle_dark, GINT_TO_POINTER(dark)); }

static gboolean idle_title(gpointer data) {
    char *t = data;
    if (win) gtk_window_set_title(win, t);
    g_free(t);
    return G_SOURCE_REMOVE;
}
void shell_set_title(const char *title) { g_idle_add(idle_title, g_strdup(title)); }

// --- video nativo ---
typedef struct { char *stream; void *paintable; double x, y, w, h; int visible, mirror, contain; } VideoOp;

static void video_op_free(VideoOp *op) { g_free(op->stream); g_free(op); }

static gboolean idle_video_attach(gpointer data) {
    VideoOp *op = data;
    // op->paintable es el sink de GStreamer: su GdkPaintable solo se puede pedir en este hilo.
    GdkPaintable *paintable = NULL;
    GObject *sink = op->paintable;
    if (sink && g_object_class_find_property(G_OBJECT_GET_CLASS(sink), "paintable")) g_object_get(sink, "paintable", &paintable, NULL);
    if (video_layer && paintable) {
        GtkWidget *old = g_hash_table_lookup(video_tiles, op->stream);
        if (old) {
            gtk_picture_set_paintable(GTK_PICTURE(old), paintable);
        } else {
            GtkWidget *pic = gtk_picture_new_for_paintable(paintable);
            gtk_picture_set_content_fit(GTK_PICTURE(pic), GTK_CONTENT_FIT_COVER);
            gtk_widget_add_css_class(pic, "hv-video");
            gtk_widget_set_overflow(pic, GTK_OVERFLOW_HIDDEN);
            gtk_widget_set_can_target(pic, FALSE);
            gtk_widget_set_visible(pic, FALSE);
            gtk_fixed_put(GTK_FIXED(video_layer), pic, 0, 0);
            g_hash_table_insert(video_tiles, g_strdup(op->stream), pic);
        }
    }
    if (paintable) g_object_unref(paintable);
    if (sink) g_object_unref(sink);
    video_op_free(op);
    return G_SOURCE_REMOVE;
}

void shell_video_attach(const char *stream, void *paintable) {
    if (!paintable) return; // en realidad, el sink de GStreamer (ver idle_video_attach)
    VideoOp *op = g_new0(VideoOp, 1);
    op->stream = g_strdup(stream);
    op->paintable = paintable;
    g_idle_add(idle_video_attach, op);
}

static gboolean idle_video_place(gpointer data) {
    VideoOp *op = data;
    GtkWidget *pic = video_tiles ? g_hash_table_lookup(video_tiles, op->stream) : NULL;
    if (pic) {
        double z = view ? webkit_web_view_get_zoom_level(view) : 1.0;
        int w = (int)(op->w * z + 0.5), h = (int)(op->h * z + 0.5);
        if (op->visible && w > 0 && h > 0) {
            gtk_widget_set_size_request(pic, w, h);
            gtk_picture_set_content_fit(GTK_PICTURE(pic), op->contain ? GTK_CONTENT_FIT_CONTAIN : GTK_CONTENT_FIT_COVER);
            GskTransform *t = gsk_transform_translate(NULL, &GRAPHENE_POINT_INIT((float)(op->x * z + (op->mirror ? w : 0)), (float)(op->y * z)));
            if (op->mirror) t = gsk_transform_scale(t, -1.0f, 1.0f);
            gtk_fixed_set_child_transform(GTK_FIXED(video_layer), pic, t);
            gsk_transform_unref(t);
            gtk_widget_set_visible(pic, TRUE);
        } else {
            gtk_widget_set_visible(pic, FALSE);
        }
    }
    video_op_free(op);
    return G_SOURCE_REMOVE;
}

void shell_video_place(const char *stream, double x, double y, double w, double h, int visible, int mirror, int contain) {
    VideoOp *op = g_new0(VideoOp, 1);
    op->stream = g_strdup(stream);
    op->x = x; op->y = y; op->w = w; op->h = h;
    op->visible = visible; op->mirror = mirror; op->contain = contain;
    g_idle_add(idle_video_place, op);
}

static gboolean idle_video_clear(gpointer data) {
    if (video_tiles) {
        GHashTableIter it;
        gpointer k, v;
        g_hash_table_iter_init(&it, video_tiles);
        while (g_hash_table_iter_next(&it, &k, &v)) gtk_fixed_remove(GTK_FIXED(video_layer), GTK_WIDGET(v));
        g_hash_table_remove_all(video_tiles);
    }
    return G_SOURCE_REMOVE;
}

void shell_video_clear(void) { g_idle_add(idle_video_clear, NULL); }

// --- diagnóstico: captura de la ventana completa (WebView + video nativo) ---
static gboolean idle_win_snapshot(gpointer data) {
    char *path = data;
    if (win && gtk_widget_get_mapped(GTK_WIDGET(win))) {
        GtkWidget *w = GTK_WIDGET(win);
        int width = gtk_widget_get_width(w), height = gtk_widget_get_height(w);
        GdkPaintable *p = gtk_widget_paintable_new(w);
        GtkSnapshot *snap = gtk_snapshot_new();
        gdk_paintable_snapshot(p, snap, width, height);
        GskRenderNode *node = gtk_snapshot_free_to_node(snap);
        if (node) {
            GskRenderer *r = gtk_native_get_renderer(GTK_NATIVE(win));
            GdkTexture *tex = gsk_renderer_render_texture(r, node, &GRAPHENE_RECT_INIT(0, 0, width, height));
            if (tex) { gdk_texture_save_to_png(tex, path); g_object_unref(tex); }
            gsk_render_node_unref(node);
        }
        g_object_unref(p);
    }
    g_free(path);
    return G_SOURCE_REMOVE;
}

void shell_window_snapshot(const char *path) { g_idle_add(idle_win_snapshot, g_strdup(path)); }

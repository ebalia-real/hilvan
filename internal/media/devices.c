//go:build linux && !android

// Enumeración de dispositivos multimedia con GstDeviceMonitor.
#include <gst/gst.h>
#include <string.h>
#include "media.h"

static gsize gst_ready = 0;

void media_init(void) {
    if (g_once_init_enter(&gst_ready)) {
        gst_init(NULL, NULL);
        g_once_init_leave(&gst_ready, 1);
    }
}

// json_escape escapa una cadena UTF-8 para JSON (sin tocar los bytes multibyte).
static char *json_escape(const char *in) {
    GString *o = g_string_new(NULL);
    for (const unsigned char *p = (const unsigned char *)in; p && *p; p++) {
        switch (*p) {
        case '"': g_string_append(o, "\\\""); break;
        case '\\': g_string_append(o, "\\\\"); break;
        case '\n': g_string_append(o, "\\n"); break;
        case '\t': g_string_append(o, "\\t"); break;
        default:
            if (*p < 0x20) g_string_append_printf(o, "\\u%04x", *p);
            else g_string_append_c(o, *p);
        }
    }
    return g_string_free(o, FALSE);
}

// media_list_devices devuelve una cadena JSON con los dispositivos (liberar con g_free).
char *media_list_devices(void) {
    media_init();
    GstDeviceMonitor *mon = gst_device_monitor_new();
    gst_device_monitor_add_filter(mon, "Audio/Source", NULL);
    gst_device_monitor_add_filter(mon, "Audio/Sink", NULL);
    gst_device_monitor_add_filter(mon, "Video/Source", NULL);
    GString *out = g_string_new("[");
    if (!gst_device_monitor_start(mon)) {
        g_string_append(out, "]");
        g_object_unref(mon);
        return g_string_free(out, FALSE);
    }
    GList *devs = gst_device_monitor_get_devices(mon);
    int first = 1;
    for (GList *l = devs; l; l = l->next) {
        GstDevice *d = GST_DEVICE(l->data);
        char *name = gst_device_get_display_name(d);
        char *klass = gst_device_get_device_class(d);
        GstStructure *props = gst_device_get_properties(d);
        const char *id = NULL;
        if (props) {
            id = gst_structure_get_string(props, "node.name");
            if (!id) id = gst_structure_get_string(props, "device.path");
            if (!id) id = gst_structure_get_string(props, "api.v4l2.path");
            if (!id) id = gst_structure_get_string(props, "device.name");
        }
        const char *kind = strstr(klass, "Video/Source") ? "videoinput" : strstr(klass, "Audio/Source") ? "audioinput" : "audiooutput";
        gboolean is_default = FALSE;
        if (props) gst_structure_get_boolean(props, "is-default", &is_default);
        char *jname = json_escape(name ? name : "");
        char *jid = json_escape(id ? id : (name ? name : ""));
        g_string_append_printf(out, "%s{\"id\":\"%s\",\"label\":\"%s\",\"kind\":\"%s\",\"default\":%s}",
            first ? "" : ",", jid, jname, kind, is_default ? "true" : "false");
        first = 0;
        g_free(jname);
        g_free(jid);
        g_free(name);
        g_free(klass);
        if (props) gst_structure_free(props);
    }
    g_list_free_full(devs, gst_object_unref);
    gst_device_monitor_stop(mon);
    g_object_unref(mon);
    g_string_append(out, "]");
    return g_string_free(out, FALSE);
}

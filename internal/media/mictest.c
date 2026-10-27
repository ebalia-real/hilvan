//go:build linux && !android

// Prueba de micrófono: captura del dispositivo elegido, procesamiento (ruido/eco/ganancia),
#define GLIB_DISABLE_DEPRECATION_WARNINGS
// medidor de nivel y, opcionalmente, retorno por el altavoz elegido.
#include <gst/gst.h>
#include <string.h>
#include <stdio.h>
#include <math.h>
#include "media.h"

static GstElement *mt_pipe = NULL;
static GMutex mt_lock;

GstDevice *media_find_device(const char *klass, const char *id) {
    if (!id || !*id) return NULL;
    GstDeviceMonitor *mon = gst_device_monitor_new();
    gst_device_monitor_add_filter(mon, klass, NULL);
    if (!gst_device_monitor_start(mon)) { g_object_unref(mon); return NULL; }
    GstDevice *found = NULL;
    GList *devs = gst_device_monitor_get_devices(mon);
    for (GList *l = devs; l && !found; l = l->next) {
        GstDevice *d = GST_DEVICE(l->data);
        GstStructure *props = gst_device_get_properties(d);
        const char *did = NULL;
        if (props) {
            did = gst_structure_get_string(props, "node.name");
            if (!did) did = gst_structure_get_string(props, "device.path");
            if (!did) did = gst_structure_get_string(props, "api.v4l2.path");
            if (!did) did = gst_structure_get_string(props, "device.name");
        }
        char *name = gst_device_get_display_name(d);
        if ((did && strcmp(did, id) == 0) || (name && strcmp(name, id) == 0)) found = gst_object_ref(d);
        g_free(name);
        if (props) gst_structure_free(props);
    }
    g_list_free_full(devs, gst_object_unref);
    gst_device_monitor_stop(mon);
    g_object_unref(mon);
    return found;
}

// make_source/make_sink crean el elemento del dispositivo (o el predeterminado).
GstElement *media_make_audio_source(const char *id) {
    GstDevice *d = media_find_device("Audio/Source", id);
    GstElement *e = NULL;
    if (d) { e = gst_device_create_element(d, NULL); gst_object_unref(d); }
    if (!e) e = gst_element_factory_make("autoaudiosrc", NULL);
    return e;
}

GstElement *media_make_audio_sink(const char *id) {
    GstDevice *d = media_find_device("Audio/Sink", id);
    GstElement *e = NULL;
    if (d) { e = gst_device_create_element(d, NULL); gst_object_unref(d); }
    if (!e) e = gst_element_factory_make("autoaudiosink", NULL);
    return e;
}

GstElement *media_make_video_source(const char *id) {
    GstDevice *d = media_find_device("Video/Source", id);
    GstElement *e = NULL;
    if (d) { e = gst_device_create_element(d, NULL); gst_object_unref(d); }
    if (!e) e = gst_element_factory_make("autovideosrc", NULL);
    return e;
}

int media_mictest_start(const char *mic, const char *speaker, double gain, double out_gain, int loopback, int ns, int ec, int agc, int ns_level) {
    media_init();
    g_mutex_lock(&mt_lock);
    if (mt_pipe) {
        gst_element_set_state(mt_pipe, GST_STATE_NULL);
        gst_object_unref(mt_pipe);
        mt_pipe = NULL;
    }
    GstElement *pipe = gst_pipeline_new("mictest");
    GstElement *src = media_make_audio_source(mic);
    GstElement *conv = gst_element_factory_make("audioconvert", NULL);
    GstElement *res = gst_element_factory_make("audioresample", NULL);
    GstElement *caps = gst_element_factory_make("capsfilter", NULL);
    GstCaps *c = gst_caps_from_string("audio/x-raw,format=S16LE,rate=48000,channels=1");
    g_object_set(caps, "caps", c, NULL);
    gst_caps_unref(c);
    GstElement *dsp = NULL;
    if (ns || agc) {
        dsp = gst_element_factory_make("webrtcdsp", NULL);
        if (dsp) {
            g_object_set(dsp, "echo-cancel", FALSE, "noise-suppression", ns ? TRUE : FALSE,
                         "gain-control", agc ? TRUE : FALSE, "high-pass-filter", TRUE, NULL);
            if (ns) g_object_set(dsp, "noise-suppression-level", ns_level, NULL);
        }
    }
    GstElement *vol = gst_element_factory_make("volume", NULL);
    g_object_set(vol, "volume", gain, NULL);
    GstElement *level = gst_element_factory_make("level", "meter");
    g_object_set(level, "interval", (guint64)50 * GST_MSECOND, "post-messages", TRUE, NULL);
    GstElement *tee = gst_element_factory_make("tee", NULL);
    GstElement *q1 = gst_element_factory_make("queue", NULL);
    GstElement *fake = gst_element_factory_make("fakesink", NULL);
    g_object_set(fake, "sync", FALSE, NULL);
    if (!src || !conv || !res || !caps || !vol || !level || !tee || !q1 || !fake) {
        g_mutex_unlock(&mt_lock);
        gst_object_unref(pipe);
        return 0;
    }
    gst_bin_add_many(GST_BIN(pipe), src, conv, res, caps, vol, level, tee, q1, fake, NULL);
    if (dsp) gst_bin_add(GST_BIN(pipe), dsp);
    gboolean ok;
    if (dsp) ok = gst_element_link_many(src, conv, res, caps, dsp, vol, level, tee, NULL);
    else ok = gst_element_link_many(src, conv, res, caps, vol, level, tee, NULL);
    ok = ok && gst_element_link_many(tee, q1, fake, NULL);
    if (loopback && ok) {
        GstElement *q2 = gst_element_factory_make("queue", NULL);
        g_object_set(q2, "max-size-time", (guint64)200 * GST_MSECOND, "leaky", 2, NULL);
        GstElement *ovol = gst_element_factory_make("volume", NULL);
        g_object_set(ovol, "volume", out_gain, NULL);
        GstElement *conv2 = gst_element_factory_make("audioconvert", NULL);
        GstElement *sink = media_make_audio_sink(speaker);
        g_object_set(sink, "sync", FALSE, NULL);
        gst_bin_add_many(GST_BIN(pipe), q2, ovol, conv2, sink, NULL);
        ok = gst_element_link_many(tee, q2, ovol, conv2, sink, NULL);
    }
    if (!ok || gst_element_set_state(pipe, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE) {
        gst_element_set_state(pipe, GST_STATE_NULL);
        gst_object_unref(pipe);
        g_mutex_unlock(&mt_lock);
        return 0;
    }
    mt_pipe = pipe;
    g_mutex_unlock(&mt_lock);
    return 1;
}

void media_mictest_stop(void) {
    g_mutex_lock(&mt_lock);
    if (mt_pipe) {
        gst_element_set_state(mt_pipe, GST_STATE_NULL);
        gst_object_unref(mt_pipe);
        mt_pipe = NULL;
    }
    g_mutex_unlock(&mt_lock);
}

// media_mictest_poll espera hasta timeout_ms un mensaje de nivel.
// Devuelve 1 con rms/peak (dB), 0 si no hubo dato, -1 si terminó o hubo error.
int media_mictest_poll(int timeout_ms, double *rms, double *peak) {
    g_mutex_lock(&mt_lock);
    GstElement *pipe = mt_pipe ? gst_object_ref(mt_pipe) : NULL;
    g_mutex_unlock(&mt_lock);
    if (!pipe) return -1;
    GstBus *bus = gst_element_get_bus(pipe);
    GstMessage *msg = gst_bus_timed_pop_filtered(bus, (GstClockTime)timeout_ms * GST_MSECOND,
        GST_MESSAGE_ELEMENT | GST_MESSAGE_ERROR | GST_MESSAGE_EOS);
    int ret = 0;
    if (msg) {
        if (GST_MESSAGE_TYPE(msg) == GST_MESSAGE_ELEMENT) {
            const GstStructure *s = gst_message_get_structure(msg);
            if (s && gst_structure_has_name(s, "level")) {
                const GValue *r = gst_structure_get_value(s, "rms");
                const GValue *p = gst_structure_get_value(s, "peak");
                if (r && p) {
                    GValueArray *ra = g_value_get_boxed(r);
                    GValueArray *pa = g_value_get_boxed(p);
                    if (ra && ra->n_values > 0 && pa && pa->n_values > 0) {
                        *rms = g_value_get_double(g_value_array_get_nth(ra, 0));
                        *peak = g_value_get_double(g_value_array_get_nth(pa, 0));
                        ret = 1;
                    }
                }
            }
        } else {
            ret = -1;
        }
        gst_message_unref(msg);
    }
    gst_object_unref(bus);
    gst_object_unref(pipe);
    return ret;
}

// media_snapshot guarda un cuadro JPEG de un nodo de video de PipeWire (miniaturas de pantallas).
int media_snapshot(unsigned int node, int width, int height, const char *path, int timeout_ms) {
    media_init();
    char desc[1024];
    snprintf(desc, sizeof desc,
        "pipewiresrc path=%u do-timestamp=true ! videoconvert ! videoscale add-borders=true ! video/x-raw,width=%d,height=%d,pixel-aspect-ratio=1/1 ! "
        "jpegenc quality=80 snapshot=true ! filesink location=\"%s\"", node, width > 0 ? width : 384, height > 0 ? height : 216, path);
    GError *err = NULL;
    GstElement *pipe = gst_parse_launch(desc, &err);
    if (!pipe) { if (err) g_error_free(err); return 0; }
    gst_element_set_state(pipe, GST_STATE_PLAYING);
    GstBus *bus = gst_element_get_bus(pipe);
    GstMessage *msg = gst_bus_timed_pop_filtered(bus, (GstClockTime)timeout_ms * GST_MSECOND, GST_MESSAGE_EOS | GST_MESSAGE_ERROR);
    int ok = msg && GST_MESSAGE_TYPE(msg) == GST_MESSAGE_EOS;
    if (msg && GST_MESSAGE_TYPE(msg) == GST_MESSAGE_ERROR) {
        GError *e = NULL; gchar *dbg = NULL;
        gst_message_parse_error(msg, &e, &dbg);
        g_warning("hilvan: miniatura: %s (%s)", e ? e->message : "?", dbg ? dbg : "");
        if (e) g_error_free(e);
        g_free(dbg);
    } else if (!msg) g_warning("hilvan: miniatura: tiempo agotado");
    if (msg) gst_message_unref(msg);
    gst_object_unref(bus);
    gst_element_set_state(pipe, GST_STATE_NULL);
    gst_object_unref(pipe);
    return ok;
}

static int wait_async(GstBus *bus, int timeout_ms) {
    GstMessage *msg = gst_bus_timed_pop_filtered(bus, (GstClockTime)timeout_ms * GST_MSECOND, GST_MESSAGE_ASYNC_DONE | GST_MESSAGE_ERROR);
    int ok = msg && GST_MESSAGE_TYPE(msg) == GST_MESSAGE_ASYNC_DONE;
    if (msg) gst_message_unref(msg);
    return ok;
}

// media_video_thumb guarda en out un cuadro JPEG (de hasta max_w×max_h) de un archivo de video,
// y devuelve su tamaño de pantalla (ya rotado según los metadatos) y su duración.
int media_video_thumb(const char *path, const char *out, int max_w, int max_h, int timeout_ms, int *w, int *h, long long *dur_ms) {
    media_init();
    *w = *h = 0; *dur_ms = 0;
    gchar *uri = gst_filename_to_uri(path, NULL);
    GstElement *pb = uri ? gst_element_factory_make("playbin", NULL) : NULL;
    if (!pb) { g_free(uri); return 0; }
    g_object_set(pb, "uri", uri, "video-sink", gst_element_factory_make("fakesink", NULL),
        "audio-sink", gst_element_factory_make("fakesink", NULL), "flags", 0x1 /* solo video */, NULL);
    g_free(uri);
    // Los videos de teléfono traen la rotación como metadato: aplicarla.
    GstElement *flip = gst_element_factory_make("videoflip", NULL);
    if (flip) { gst_util_set_object_arg(G_OBJECT(flip), "video-direction", "auto"); g_object_set(pb, "video-filter", flip, NULL); }
    GstBus *bus = gst_element_get_bus(pb);
    int ok = 0;
    if (gst_element_set_state(pb, GST_STATE_PAUSED) == GST_STATE_CHANGE_FAILURE || !wait_async(bus, timeout_ms)) goto done;
    gint64 dur = -1;
    if (gst_element_query_duration(pb, GST_FORMAT_TIME, &dur) && dur > 0) {
        *dur_ms = dur / GST_MSECOND;
        // El primer cuadro suele ser negro: tomar el de 1 s (o el 10 % en videos cortos).
        gint64 pos = MIN(GST_SECOND, dur / 10);
        if (pos > 0 && gst_element_seek_simple(pb, GST_FORMAT_TIME, GST_SEEK_FLAG_FLUSH | GST_SEEK_FLAG_KEY_UNIT, pos)) wait_async(bus, timeout_ms);
    }
    GstSample *cur = NULL;
    g_object_get(pb, "sample", &cur, NULL);
    if (!cur) goto done;
    GstStructure *s = gst_caps_get_structure(gst_sample_get_caps(cur), 0);
    int vw = 0, vh = 0, pn = 1, pd = 1;
    gst_structure_get_int(s, "width", &vw);
    gst_structure_get_int(s, "height", &vh);
    if (!gst_structure_get_fraction(s, "pixel-aspect-ratio", &pn, &pd) || pn <= 0 || pd <= 0) pn = pd = 1;
    gst_sample_unref(cur);
    if (vw <= 0 || vh <= 0) goto done;
    double dw = (double)vw * pn / pd, dh = vh;
    *w = (int)(dw + 0.5); *h = vh;
    double sc = MIN(1.0, MIN(max_w / dw, max_h / dh));
    int tw = MAX(2, (int)(dw * sc + 0.5)) & ~1, th = MAX(2, (int)(dh * sc + 0.5)) & ~1;
    GstCaps *jc = gst_caps_new_simple("image/jpeg", "width", G_TYPE_INT, tw, "height", G_TYPE_INT, th,
        "pixel-aspect-ratio", GST_TYPE_FRACTION, 1, 1, NULL);
    GstSample *js = NULL;
    g_signal_emit_by_name(pb, "convert-sample", jc, &js);
    gst_caps_unref(jc);
    if (!js) goto done;
    GstBuffer *b = gst_sample_get_buffer(js);
    GstMapInfo mi;
    if (b && gst_buffer_map(b, &mi, GST_MAP_READ)) {
        FILE *f = fopen(out, "wb");
        if (f) { ok = fwrite(mi.data, 1, mi.size, f) == mi.size; if (fclose(f) != 0) ok = 0; }
        gst_buffer_unmap(b, &mi);
    }
    gst_sample_unref(js);
done:
    gst_object_unref(bus);
    gst_element_set_state(pb, GST_STATE_NULL);
    gst_object_unref(pb);
    return ok;
}

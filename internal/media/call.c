//go:build linux && !android

// Motor de llamadas nativo: GStreamer webrtcbin (Opus + VP8), baja latencia.
#define GST_USE_UNSTABLE_API
#define GLIB_DISABLE_DEPRECATION_WARNINGS
#include <gst/gst.h>
#include <gst/sdp/sdp.h>
#include <gst/webrtc/webrtc.h>
#include <string.h>
#include <stdio.h>
#include "media.h"
#include "call.h"

extern void goCallCandidate(char *id, int mline, char *cand);
extern void goCallState(char *id, char *kind, char *state);
extern void goCallNegotiationNeeded(char *id);
extern void goCallStream(char *id, char *stream, char *kind);
extern void goCallLevel(char *id, char *who, double rms);

struct HvCall {
    char *id;
    GstElement *pipe;
    GstElement *webrtc;
    // envío de audio
    GstElement *aouter, *asend, *asrc;   // bin externo, cadena de procesamiento, fuente (micrófono)
    GstElement *micvol, *avalve, *aenc, *sendmix;
    GstElement *sharebin;                 // audio compartido (pantalla / aplicación)
    GstPad *share_pad;
    // envío de video
    GstElement *vbin, *camsrc, *vsel, *vcaps, *vvalve, *venc, *local_sink;
    GstPad *cam_pad, *screen_pad;
    GstElement *screen_src;
    // recepción
    GstElement *outbin, *spkmix, *spkvol, *spksink, *remote_sink;
    GPtrArray *uvols;                     // volumen por persona (ramas de audio entrantes)
    double remote_gain;
    HvCallConfig cfg;
    gboolean sources_linked, closed;
    GMutex lock;
    int remote_video_n;
};

static gboolean has_display(void) {
    return g_getenv("WAYLAND_DISPLAY") || g_getenv("DISPLAY");
}

static GstElement *make(const char *factory, const char *name) {
    GstElement *e = gst_element_factory_make(factory, name);
    if (!e) g_warning("hilvan: falta el elemento GStreamer '%s'", factory);
    return e;
}

// Los sinks de video se agregan con la llamada ya andando (video remoto, pantalla en una llamada de voz):
// sin async, para que no esperen un cuadro de "preroll" mientras la fuente en vivo espera a PLAYING.
static GstElement *video_sink_for(HvCall *c, const char *name) {
    if (!c->cfg.headless && has_display()) {
        GstElement *s = gst_element_factory_make("gtk4paintablesink", name);
        if (s) { g_object_set(s, "async", FALSE, NULL); return s; }
    }
    GstElement *f = gst_element_factory_make("fakesink", name);
    g_object_set(f, "sync", FALSE, "async", FALSE, NULL);
    return f;
}

static GstElement *parse_bin(const char *what, const char *desc, gboolean ghost) {
    GError *err = NULL;
    GstElement *bin = gst_parse_bin_from_description(desc, ghost, &err);
    if (!bin) {
        g_warning("hilvan: %s: %s", what, err ? err->message : "?");
        if (err) g_error_free(err);
    }
    return bin;
}

static GstElement *child(GstElement *bin, const char *name) {
    GstElement *e = gst_bin_get_by_name(GST_BIN(bin), name);
    if (e) gst_object_unref(e); // el bin conserva la referencia
    return e;
}

static void fmt_double(char *buf, size_t n, double v) { g_ascii_formatd(buf, n, "%.3f", v); } // sin coma decimal del locale

// --- señales de webrtcbin ---
static void on_ice_candidate(GstElement *w, guint mline, gchar *cand, gpointer data) {
    HvCall *c = data;
    if (!c->closed) goCallCandidate(c->id, (int)mline, cand);
}

static void on_negotiation_needed(GstElement *w, gpointer data) {
    HvCall *c = data;
    if (!c->closed) goCallNegotiationNeeded(c->id);
}

static void on_gathering_state(GstElement *w, GParamSpec *p, gpointer data) {
    HvCall *c = data;
    GstWebRTCICEGatheringState st;
    g_object_get(w, "ice-gathering-state", &st, NULL);
    if (st == GST_WEBRTC_ICE_GATHERING_STATE_COMPLETE && !c->closed) goCallCandidate(c->id, -1, "");
}

static void on_conn_state(GstElement *w, GParamSpec *p, gpointer data) {
    HvCall *c = data;
    GstWebRTCPeerConnectionState st;
    g_object_get(w, "connection-state", &st, NULL);
    const char *s = "new";
    switch (st) {
    case GST_WEBRTC_PEER_CONNECTION_STATE_CONNECTING: s = "connecting"; break;
    case GST_WEBRTC_PEER_CONNECTION_STATE_CONNECTED: s = "connected"; break;
    case GST_WEBRTC_PEER_CONNECTION_STATE_DISCONNECTED: s = "disconnected"; break;
    case GST_WEBRTC_PEER_CONNECTION_STATE_FAILED: s = "failed"; break;
    case GST_WEBRTC_PEER_CONNECTION_STATE_CLOSED: s = "closed"; break;
    default: break;
    }
    if (!c->closed) goCallState(c->id, "connection", (char *)s);
}

static void on_ice_state(GstElement *w, GParamSpec *p, gpointer data) {
    HvCall *c = data;
    GstWebRTCICEConnectionState st;
    g_object_get(w, "ice-connection-state", &st, NULL);
    const char *s = "new";
    switch (st) {
    case GST_WEBRTC_ICE_CONNECTION_STATE_CHECKING: s = "checking"; break;
    case GST_WEBRTC_ICE_CONNECTION_STATE_CONNECTED: s = "connected"; break;
    case GST_WEBRTC_ICE_CONNECTION_STATE_COMPLETED: s = "completed"; break;
    case GST_WEBRTC_ICE_CONNECTION_STATE_FAILED: s = "failed"; break;
    case GST_WEBRTC_ICE_CONNECTION_STATE_DISCONNECTED: s = "disconnected"; break;
    case GST_WEBRTC_ICE_CONNECTION_STATE_CLOSED: s = "closed"; break;
    default: break;
    }
    if (!c->closed) goCallState(c->id, "ice", (char *)s);
}

// --- salida: silencio continuo + voces remotas → volumen → medidor → sondas de eco → altavoz ---
// Las sondas existen desde el principio: la cancelación de eco del micrófono (hvprobe) y la del
// audio compartido del sistema (hvprobe2, para no devolverle al otro su propia voz).
static GstElement *make_speaker(HvCall *c, const char *dev) {
    GstElement *sink;
    if (c->cfg.test_media) {
        sink = make("fakesink", NULL);
        g_object_set(sink, "sync", TRUE, NULL);
        return sink;
    }
    sink = media_make_audio_sink(dev);
    if (c->cfg.low_latency && g_object_class_find_property(G_OBJECT_GET_CLASS(sink), "buffer-time"))
        g_object_set(sink, "buffer-time", (gint64)40000, "latency-time", (gint64)10000, NULL);
    return sink;
}

static gboolean build_output(HvCall *c) {
    char vol[G_ASCII_DTOSTR_BUF_SIZE];
    fmt_double(vol, sizeof vol, c->cfg.spk_gain >= 0 ? c->cfg.spk_gain : 1.0);
    GstElementFactory *pf = gst_element_factory_find("webrtcechoprobe");
    gboolean probe2 = pf != NULL;
    if (pf) gst_object_unref(pf);
    char desc[1024];
    snprintf(desc, sizeof desc,
        "audiotestsrc is-live=true wave=silence samplesperbuffer=480 ! audio/x-raw,rate=48000,channels=1 ! spkmix. "
        "audiomixer name=spkmix output-buffer-duration=10000000 ! volume name=spkvol volume=%s ! "
        "level name=remotelevel interval=100000000 post-messages=true ! %s%s audioconvert ! audioresample",
        vol, c->cfg.ec ? "webrtcechoprobe name=hvprobe ! " : "", probe2 ? "webrtcechoprobe name=hvprobe2 ! " : "");
    GstElement *bin = parse_bin("salida de audio", desc, TRUE);
    if (!bin) return FALSE;
    c->spksink = make_speaker(c, c->cfg.speaker);
    gst_bin_add_many(GST_BIN(c->pipe), bin, c->spksink, NULL);
    gst_element_link(bin, c->spksink);
    c->outbin = bin;
    c->spkmix = child(bin, "spkmix");
    c->spkvol = child(bin, "spkvol");
    return TRUE;
}

// --- recepción: decodificar lo que llega ---
static void on_incoming_pad(GstElement *w, GstPad *pad, gpointer data) {
    HvCall *c = data;
    if (GST_PAD_DIRECTION(pad) != GST_PAD_SRC) return;
    GstCaps *caps = gst_pad_get_current_caps(pad);
    if (!caps) caps = gst_pad_query_caps(pad, NULL);
    GstStructure *s = gst_caps_get_structure(caps, 0);
    const char *media = gst_structure_get_string(s, "media");
    const char *enc = gst_structure_get_string(s, "encoding-name");
    char desc[1024];
    gboolean is_video = media && strcmp(media, "video") == 0;
    if (!is_video) {
        char g[G_ASCII_DTOSTR_BUF_SIZE];
        fmt_double(g, sizeof g, c->remote_gain);
        snprintf(desc, sizeof desc, "queue max-size-time=200000000 leaky=downstream ! rtpopusdepay ! opusdec plc=true use-inband-fec=true ! "
            "audioconvert ! audioresample ! volume name=uvol volume=%s ! capsfilter caps=\"audio/x-raw,rate=48000,channels=1\"", g);
    } else {
        const char *dec = "rtpvp8depay ! vp8dec";
        if (enc && g_ascii_strcasecmp(enc, "VP9") == 0) dec = "rtpvp9depay ! vp9dec";
        else if (enc && g_ascii_strcasecmp(enc, "H264") == 0) dec = "rtph264depay ! h264parse ! avdec_h264";
        else if (enc && g_ascii_strcasecmp(enc, "AV1") == 0) dec = "rtpav1depay ! av1parse ! dav1ddec";
        snprintf(desc, sizeof desc, "queue max-size-buffers=3 leaky=downstream ! %s ! videoconvert ! queue max-size-buffers=1 leaky=downstream", dec);
    }
    gst_caps_unref(caps);
    GstElement *bin = parse_bin("rama de recepción", desc, TRUE);
    if (!bin) return;
    char stream[32];
    if (!is_video) {
        snprintf(stream, sizeof stream, "remote-audio");
        gst_bin_add(GST_BIN(c->pipe), bin);
        GstElement *uv = child(bin, "uvol");
        if (uv) { g_mutex_lock(&c->lock); g_ptr_array_add(c->uvols, gst_object_ref(uv)); g_mutex_unlock(&c->lock); }
        if (!c->spkmix || !gst_element_link(bin, c->spkmix)) g_warning("hilvan: no se pudo enlazar el audio recibido");
    } else {
        snprintf(stream, sizeof stream, c->remote_video_n == 0 ? "remote" : "remote-%d", c->remote_video_n);
        c->remote_video_n++;
        GstElement *sink = video_sink_for(c, stream);
        if (c->remote_video_n == 1) c->remote_sink = sink;
        gst_bin_add_many(GST_BIN(c->pipe), bin, sink, NULL);
        gst_element_link(bin, sink);
        gst_element_sync_state_with_parent(sink);
    }
    gst_element_sync_state_with_parent(bin);
    GstPad *sinkpad = gst_element_get_static_pad(bin, "sink");
    if (gst_pad_link(pad, sinkpad) != GST_PAD_LINK_OK) g_warning("hilvan: no se pudo enlazar la rama de recepción");
    gst_object_unref(sinkpad);
    goCallStream(c->id, stream, is_video ? "video" : "audio");
}

// --- envío de audio: micrófono → DSP → volumen → válvula → medidor → mezclador (+ audio compartido) → Opus ---
static GstElement *make_mic(HvCall *c, const char *dev) {
    if (c->cfg.test_media) {
        GstElement *src = make("audiotestsrc", NULL);
        g_object_set(src, "is-live", TRUE, "wave", 0, "freq", 440.0, "volume", 0.1, NULL);
        return src;
    }
    return media_make_audio_source(dev);
}

static GstElement *build_audio_send(HvCall *c) {
    char desc[2048];
    char dsp[256] = "";
    if (c->cfg.ns || c->cfg.ec || c->cfg.agc) {
        snprintf(dsp, sizeof dsp, "webrtcdsp name=dsp echo-cancel=%s noise-suppression=%s noise-suppression-level=%d gain-control=%s high-pass-filter=true probe=hvprobe ! ",
                 c->cfg.ec ? "true" : "false", c->cfg.ns ? "true" : "false", c->cfg.ns_level, c->cfg.agc ? "true" : "false");
    }
    int pt = c->cfg.audio_pt > 0 ? c->cfg.audio_pt : 111;
    char vol[G_ASCII_DTOSTR_BUF_SIZE];
    fmt_double(vol, sizeof vol, c->cfg.mic_gain >= 0 ? c->cfg.mic_gain : 1.0);
    snprintf(desc, sizeof desc,
        "audioconvert ! audioresample ! audio/x-raw,rate=48000,channels=1 ! %s"
        "volume name=micvol volume=%s ! valve name=avalve drop=false ! level name=locallevel interval=100000000 post-messages=true ! "
        "audioconvert ! audio/x-raw,format=S16LE,rate=48000,channels=1 ! "
        "audiomixer name=sendmix output-buffer-duration=10000000 ! audioconvert ! "
        "opusenc name=aenc bitrate=%d inband-fec=true packet-loss-percentage=10 frame-size=%d audio-type=voice ! "
        "rtpopuspay pt=%d ! capsfilter caps=\"application/x-rtp,media=audio,encoding-name=OPUS,payload=%d,clock-rate=48000\"",
        dsp, vol, c->cfg.audio_bitrate > 0 ? c->cfg.audio_bitrate : 48000, c->cfg.low_latency ? 10 : 20, pt, pt);
    GstElement *bin = parse_bin("rama de audio", desc, TRUE);
    if (!bin) return NULL;
    GstElement *src = make_mic(c, c->cfg.mic);
    GstElement *outer = gst_bin_new(NULL);
    gst_bin_add_many(GST_BIN(outer), src, bin, NULL);
    gst_element_link(src, bin);
    GstPad *srcpad = gst_element_get_static_pad(bin, "src");
    gst_element_add_pad(outer, gst_ghost_pad_new("src", srcpad));
    gst_object_unref(srcpad);
    c->aouter = outer;
    c->asend = bin;
    c->asrc = src;
    c->micvol = child(bin, "micvol");
    c->avalve = child(bin, "avalve");
    c->aenc = child(bin, "aenc");
    c->sendmix = child(bin, "sendmix");
    return outer;
}

// --- envío de video: cámara / pantalla → selector → escala → (vista previa | VP8) ---
static void set_video_caps(HvCall *c, int w, int h, int fps) {
    if (!c->vcaps) return;
    GstCaps *caps = gst_caps_new_simple("video/x-raw", "format", G_TYPE_STRING, "I420",
        "framerate", GST_TYPE_FRACTION, fps > 0 ? fps : 30, 1, "pixel-aspect-ratio", GST_TYPE_FRACTION, 1, 1, NULL);
    if (w > 0 && h > 0) gst_caps_set_simple(caps, "width", G_TYPE_INT, w & ~1, "height", G_TYPE_INT, h & ~1, NULL);
    g_object_set(c->vcaps, "caps", caps, NULL);
    gst_caps_unref(caps);
}

static GstElement *build_video_send(HvCall *c, gboolean camera) {
    int fps = c->cfg.video_fps > 0 ? c->cfg.video_fps : 30;
    int kbps = c->cfg.video_kbps > 0 ? c->cfg.video_kbps : 1500;
    int pt = c->cfg.video_pt > 0 ? c->cfg.video_pt : 96;
    char desc[2048];
    snprintf(desc, sizeof desc,
        "input-selector name=vsel sync-mode=1 ! videoconvert ! videoscale add-borders=true ! videorate drop-only=true ! capsfilter name=vcaps ! tee name=vtee "
        "vtee. ! queue max-size-buffers=1 leaky=downstream ! videoconvert name=previewconv "
        "vtee. ! queue max-size-buffers=2 leaky=downstream ! valve name=vvalve drop=false ! "
        "vp8enc name=venc deadline=1 cpu-used=%d end-usage=cbr target-bitrate=%d keyframe-max-dist=%d lag-in-frames=0 "
        "error-resilient=default threads=4 undershoot=95 buffer-size=1000 buffer-initial-size=500 buffer-optimal-size=600 ! "
        "rtpvp8pay pt=%d picture-id-mode=15-bit mtu=1200 ! capsfilter name=vout caps=\"application/x-rtp,media=video,encoding-name=VP8,payload=%d,clock-rate=90000\"",
        c->cfg.low_latency ? 12 : 8, kbps * 1000, fps * 3, pt, pt);
    GstElement *bin = parse_bin("rama de video", desc, FALSE);
    if (!bin) return NULL;
    c->vbin = bin;
    c->vsel = child(bin, "vsel");
    c->vcaps = child(bin, "vcaps");
    c->vvalve = child(bin, "vvalve");
    c->venc = child(bin, "venc");
    set_video_caps(c, c->cfg.video_w > 0 ? c->cfg.video_w : 1280, c->cfg.video_h > 0 ? c->cfg.video_h : 720, fps);
    GstElement *conv = child(bin, "previewconv");
    // Sin cámara (pantalla en llamada de voz) no hay vista previa local: un sink neutro.
    if (camera) {
        c->local_sink = video_sink_for(c, "local");
    } else {
        c->local_sink = gst_element_factory_make("fakesink", "local");
        g_object_set(c->local_sink, "sync", FALSE, "async", FALSE, NULL);
    }
    gst_bin_add(GST_BIN(bin), c->local_sink);
    gst_element_link(conv, c->local_sink);
    if (camera) {
        GstElement *src;
        if (c->cfg.test_media) {
            src = make("videotestsrc", NULL);
            g_object_set(src, "is-live", TRUE, "pattern", 18 /* bola */, NULL);
        } else {
            src = media_make_video_source(c->cfg.camera);
        }
        gst_bin_add(GST_BIN(bin), src);
        c->cam_pad = gst_element_request_pad_simple(c->vsel, "sink_%u");
        gst_element_link_pads(src, NULL, c->vsel, GST_PAD_NAME(c->cam_pad));
        c->camsrc = src;
    }
    // Salida del bin: el capsfilter final (explícito; buscar "el pad libre" encontraba pads internos del sink de GTK).
    GstPad *out = gst_element_get_static_pad(child(bin, "vout"), "src");
    gst_element_add_pad(bin, gst_ghost_pad_new("src", out));
    gst_object_unref(out);
    return bin;
}

HvCall *hv_call_new(HvCallConfig *cfg) {
    media_init();
    HvCall *c = g_new0(HvCall, 1);
    c->cfg = *cfg;
    c->cfg.mic = g_strdup(cfg->mic ? cfg->mic : "");
    c->cfg.speaker = g_strdup(cfg->speaker ? cfg->speaker : "");
    c->cfg.camera = g_strdup(cfg->camera ? cfg->camera : "");
    c->id = g_strdup(cfg->id);
    c->uvols = g_ptr_array_new_with_free_func(gst_object_unref);
    c->remote_gain = 1.0;
    g_mutex_init(&c->lock);
    c->pipe = gst_pipeline_new(NULL);
    // Reloj del sistema: así se puede cambiar el altavoz en plena llamada sin perder el reloj.
    GstClock *clk = gst_system_clock_obtain();
    gst_pipeline_use_clock(GST_PIPELINE(c->pipe), clk);
    gst_object_unref(clk);
    c->webrtc = make("webrtcbin", "webrtc");
    if (!c->webrtc) {
        gst_object_unref(c->pipe);
        g_free(c);
        return NULL;
    }
    g_object_set(c->webrtc, "bundle-policy", GST_WEBRTC_BUNDLE_POLICY_MAX_BUNDLE,
                 "latency", (guint)(cfg->low_latency ? 60 : 150), NULL);
    if (cfg->stun && *cfg->stun) g_object_set(c->webrtc, "stun-server", cfg->stun, NULL);
    if (cfg->force_relay) g_object_set(c->webrtc, "ice-transport-policy", GST_WEBRTC_ICE_TRANSPORT_POLICY_RELAY, NULL);
    gst_bin_add(GST_BIN(c->pipe), c->webrtc);
    for (int i = 0; i < cfg->n_turns; i++) {
        gboolean ok = FALSE;
        g_signal_emit_by_name(c->webrtc, "add-turn-server", cfg->turns[i], &ok);
    }
    g_signal_connect(c->webrtc, "on-ice-candidate", G_CALLBACK(on_ice_candidate), c);
    g_signal_connect(c->webrtc, "on-negotiation-needed", G_CALLBACK(on_negotiation_needed), c);
    g_signal_connect(c->webrtc, "pad-added", G_CALLBACK(on_incoming_pad), c);
    g_signal_connect(c->webrtc, "notify::ice-gathering-state", G_CALLBACK(on_gathering_state), c);
    g_signal_connect(c->webrtc, "notify::connection-state", G_CALLBACK(on_conn_state), c);
    g_signal_connect(c->webrtc, "notify::ice-connection-state", G_CALLBACK(on_ice_state), c);
    gst_element_set_state(c->pipe, GST_STATE_READY);
    return c;
}

// Un transceptor creado por la oferta remota puede quedar "recvonly"; al enviar lo pasamos a sendrecv.
static void ensure_sendrecv(GstPad *wp) {
    GstWebRTCRTPTransceiver *t = NULL;
    if (g_object_class_find_property(G_OBJECT_GET_CLASS(wp), "transceiver")) g_object_get(wp, "transceiver", &t, NULL);
    if (!t) return;
    GstWebRTCRTPTransceiverDirection d;
    g_object_get(t, "direction", &d, NULL);
    if (d != GST_WEBRTC_RTP_TRANSCEIVER_DIRECTION_SENDRECV) g_object_set(t, "direction", GST_WEBRTC_RTP_TRANSCEIVER_DIRECTION_SENDRECV, NULL);
    gst_object_unref(t);
}

static gboolean link_to_webrtc(HvCall *c, GstElement *bin, int mline, const char *what) {
    char padname[16];
    if (mline >= 0) snprintf(padname, sizeof padname, "sink_%d", mline);
    else snprintf(padname, sizeof padname, "sink_%%u");
    GstPad *wp = gst_element_request_pad_simple(c->webrtc, padname);
    GstPad *sp = gst_element_get_static_pad(bin, "src");
    GstPadLinkReturn lr = wp && sp ? gst_pad_link(sp, wp) : GST_PAD_LINK_REFUSED;
    if (sp) gst_object_unref(sp);
    if (lr != GST_PAD_LINK_OK) {
        g_warning("hilvan: no se pudo enlazar %s (%d)", what, lr);
        if (wp) gst_object_unref(wp);
        return FALSE;
    }
    ensure_sendrecv(wp);
    gst_object_unref(wp);
    return TRUE;
}

// hv_call_link_sources conecta micrófono (y cámara) a las m-lines indicadas (-1 = no enviar).
int hv_call_link_sources(HvCall *c, int audio_mline, int video_mline) {
    if (c->sources_linked) return 1;
    if (!c->spkmix && !build_output(c)) return 0;
    if (audio_mline >= 0) {
        GstElement *a = build_audio_send(c);
        if (!a) return 0;
        gst_bin_add(GST_BIN(c->pipe), a);
        if (!link_to_webrtc(c, a, audio_mline, "el audio")) return 0;
    }
    if (video_mline >= 0 && c->cfg.video) {
        GstElement *v = build_video_send(c, TRUE);
        if (!v) return 0;
        gst_bin_add(GST_BIN(c->pipe), v);
        if (!link_to_webrtc(c, v, video_mline, "el video")) return 0;
    }
    c->sources_linked = TRUE;
    return 1;
}

int hv_call_play(HvCall *c) {
    return gst_element_set_state(c->pipe, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE;
}

static char *desc_to_text(GstWebRTCSessionDescription *d) {
    return gst_sdp_message_as_text(d->sdp);
}

// hv_call_create crea una oferta (offer=1) o respuesta y la fija como descripción local. Devuelve el SDP (g_free).
char *hv_call_create(HvCall *c, int offer) {
    GstPromise *p = gst_promise_new();
    g_signal_emit_by_name(c->webrtc, offer ? "create-offer" : "create-answer", NULL, p);
    if (gst_promise_wait(p) != GST_PROMISE_RESULT_REPLIED) { gst_promise_unref(p); return NULL; }
    const GstStructure *r = gst_promise_get_reply(p);
    GstWebRTCSessionDescription *d = NULL;
    if (r) gst_structure_get(r, offer ? "offer" : "answer", GST_TYPE_WEBRTC_SESSION_DESCRIPTION, &d, NULL);
    gst_promise_unref(p);
    if (!d) return NULL;
    GstPromise *p2 = gst_promise_new();
    g_signal_emit_by_name(c->webrtc, "set-local-description", d, p2);
    gst_promise_wait(p2);
    gst_promise_unref(p2);
    char *txt = desc_to_text(d);
    gst_webrtc_session_description_free(d);
    return txt;
}

int hv_call_set_remote(HvCall *c, int offer, const char *sdp) {
    GstSDPMessage *msg = NULL;
    if (gst_sdp_message_new_from_text(sdp, &msg) != GST_SDP_OK) return 0;
    GstWebRTCSessionDescription *d = gst_webrtc_session_description_new(offer ? GST_WEBRTC_SDP_TYPE_OFFER : GST_WEBRTC_SDP_TYPE_ANSWER, msg);
    GstPromise *p = gst_promise_new();
    g_signal_emit_by_name(c->webrtc, "set-remote-description", d, p);
    GstPromiseResult res = gst_promise_wait(p);
    const GstStructure *r = gst_promise_get_reply(p);
    int ok = res == GST_PROMISE_RESULT_REPLIED;
    if (r && gst_structure_has_field(r, "error")) {
        GError *e = NULL;
        gst_structure_get(r, "error", G_TYPE_ERROR, &e, NULL);
        if (e) { g_warning("hilvan: set-remote-description: %s", e->message); g_error_free(e); }
        ok = 0;
    }
    gst_promise_unref(p);
    gst_webrtc_session_description_free(d);
    return ok;
}

void hv_call_add_candidate(HvCall *c, int mline, const char *cand) {
    if (!cand || !*cand) return;
    g_signal_emit_by_name(c->webrtc, "add-ice-candidate", (guint)mline, cand);
}

void hv_call_set_mic_muted(HvCall *c, int muted) {
    if (c->avalve) g_object_set(c->avalve, "drop", muted ? TRUE : FALSE, NULL);
}

static void force_keyframe(HvCall *c) {
    if (!c->venc) return;
    GstEvent *ev = gst_event_new_custom(GST_EVENT_CUSTOM_DOWNSTREAM,
        gst_structure_new("GstForceKeyUnit", "all-headers", G_TYPE_BOOLEAN, TRUE, NULL));
    GstPad *sink = gst_element_get_static_pad(c->venc, "sink");
    if (sink) { gst_pad_send_event(sink, ev); gst_object_unref(sink); } else gst_event_unref(ev);
}

void hv_call_set_video_muted(HvCall *c, int muted) {
    if (c->vvalve) g_object_set(c->vvalve, "drop", muted ? TRUE : FALSE, NULL);
    if (!muted) force_keyframe(c); // que el otro lado vea la imagen enseguida
}

void hv_call_set_volumes(HvCall *c, double mic, double spk) {
    if (c->micvol && mic >= 0) g_object_set(c->micvol, "volume", mic, NULL);
    if (c->spkvol && spk >= 0) g_object_set(c->spkvol, "volume", spk, NULL);
}

void hv_call_set_remote_volume(HvCall *c, double v) {
    g_mutex_lock(&c->lock);
    c->remote_gain = v;
    for (guint i = 0; i < c->uvols->len; i++) g_object_set(g_ptr_array_index(c->uvols, i), "volume", v, NULL);
    g_mutex_unlock(&c->lock);
}

void hv_call_set_bitrate(HvCall *c, int kbps) {
    if (c->venc && kbps > 0) g_object_set(c->venc, "target-bitrate", kbps * 1000, NULL);
}

// --- cambiar dispositivos en plena llamada ---
int hv_call_set_input(HvCall *c, const char *dev) {
    if (!c->asrc || c->cfg.test_media) return 0;
    GstElement *n = make_mic(c, dev);
    if (!n) return 0;
    GstElement *old = c->asrc;
    gst_element_set_locked_state(old, TRUE);
    gst_element_set_state(old, GST_STATE_NULL);
    gst_element_unlink(old, c->asend);
    gst_bin_remove(GST_BIN(c->aouter), old);
    gst_bin_add(GST_BIN(c->aouter), n);
    if (!gst_element_link(n, c->asend)) { g_warning("hilvan: no se pudo cambiar el micrófono"); return 0; }
    gst_element_sync_state_with_parent(n);
    c->asrc = n;
    return 1;
}

int hv_call_set_output(HvCall *c, const char *dev) {
    if (!c->spksink || !c->outbin || c->cfg.test_media) return 0;
    GstElement *n = make_speaker(c, dev);
    if (!n) return 0;
    if (g_object_class_find_property(G_OBJECT_GET_CLASS(n), "async")) g_object_set(n, "async", FALSE, NULL);
    GstElement *old = c->spksink;
    gst_element_set_locked_state(old, TRUE);
    gst_element_set_state(old, GST_STATE_NULL);
    gst_element_unlink(c->outbin, old);
    gst_bin_remove(GST_BIN(c->pipe), old);
    gst_bin_add(GST_BIN(c->pipe), n);
    if (!gst_element_link(c->outbin, n)) { g_warning("hilvan: no se pudo cambiar el altavoz"); return 0; }
    gst_element_sync_state_with_parent(n);
    c->spksink = n;
    return 1;
}

int hv_call_set_camera(HvCall *c, const char *dev) {
    if (!c->camsrc || !c->vbin || c->cfg.test_media) return 0;
    GstElement *n = media_make_video_source(dev);
    if (!n) return 0;
    GstElement *old = c->camsrc;
    gst_element_set_locked_state(old, TRUE);
    gst_element_set_state(old, GST_STATE_NULL);
    GstPad *peer = gst_pad_get_peer(c->cam_pad);
    if (peer) { gst_pad_unlink(peer, c->cam_pad); gst_object_unref(peer); }
    gst_bin_remove(GST_BIN(c->vbin), old);
    gst_bin_add(GST_BIN(c->vbin), n);
    if (!gst_element_link_pads(n, NULL, c->vsel, GST_PAD_NAME(c->cam_pad))) { g_warning("hilvan: no se pudo cambiar la cámara"); return 0; }
    gst_element_sync_state_with_parent(n);
    c->camsrc = n;
    force_keyframe(c);
    return 1;
}

// --- compartir pantalla: fuente PipeWire (del portal) en el selector de video ---
// En una llamada de voz no hay pista de video: se agrega una (y hay que renegociar). Devuelve 2 en ese caso.
int hv_call_screen_start(HvCall *c, int fd, unsigned int node, int fps, int w, int h, int kbps) {
    int added = 0;
    if (!c->vsel) {
        GstElement *v = build_video_send(c, FALSE);
        if (!v) return 0;
        gst_bin_add(GST_BIN(c->pipe), v);
        if (!link_to_webrtc(c, v, -1, "el video de la pantalla")) return 0;
        gst_element_sync_state_with_parent(v);
        added = 1;
    }
    if (c->screen_src) return 1;
    char desc[512];
    // Con fd (portal) o directo al PipeWire de la sesión (captura de GNOME: fd = -1).
    char fdp[32] = "";
    if (fd >= 0) snprintf(fdp, sizeof fdp, "fd=%d ", fd);
    snprintf(desc, sizeof desc, "pipewiresrc %spath=%u do-timestamp=true keepalive-time=1000 always-copy=false ! videoconvert", fdp, node);
    GstElement *bin = parse_bin("captura de pantalla", desc, TRUE);
    if (!bin) return 0;
    // En el mismo bin que el selector: entre bins distintos los pads no se pueden enlazar directo.
    gst_bin_add(GST_BIN(c->vbin), bin);
    c->screen_pad = gst_element_request_pad_simple(c->vsel, "sink_%u");
    GstPad *sp = gst_element_get_static_pad(bin, "src");
    GstPadLinkReturn lr = gst_pad_link(sp, c->screen_pad);
    gst_object_unref(sp);
    if (lr != GST_PAD_LINK_OK) {
        g_warning("hilvan: no se pudo enlazar la captura de pantalla (%d)", lr);
        gst_element_release_request_pad(c->vsel, c->screen_pad);
        gst_object_unref(c->screen_pad);
        c->screen_pad = NULL;
        gst_bin_remove(GST_BIN(c->vbin), bin);
        return 0;
    }
    // Primero la entrada activa y después arrancar la captura (si no, el selector la rechaza).
    g_object_set(c->vsel, "active-pad", c->screen_pad, NULL);
    c->screen_src = bin;
    set_video_caps(c, w, h, fps);
    hv_call_set_bitrate(c, kbps);
    // Pista nueva: la válvula queda cerrada hasta que el otro lado acepte la renegociación.
    if (added) g_object_set(c->vvalve, "drop", TRUE, NULL);
    gst_element_sync_state_with_parent(bin);
    if (!added) hv_call_set_video_muted(c, 0);
    return added ? 2 : 1;
}

void hv_call_screen_quality(HvCall *c, int fps, int w, int h, int kbps) {
    if (!c->screen_src) return;
    set_video_caps(c, w, h, fps);
    hv_call_set_bitrate(c, kbps);
    force_keyframe(c);
}

void hv_call_screen_stop(HvCall *c) {
    if (!c->screen_src) return;
    if (c->cam_pad) g_object_set(c->vsel, "active-pad", c->cam_pad, NULL);
    else if (c->vvalve) g_object_set(c->vvalve, "drop", TRUE, NULL); // llamada de voz: no hay cámara que mostrar
    gst_element_set_locked_state(c->screen_src, TRUE);
    gst_element_set_state(c->screen_src, GST_STATE_NULL);
    GstPad *peer = gst_pad_get_peer(c->screen_pad);
    if (peer) { gst_pad_unlink(peer, c->screen_pad); gst_object_unref(peer); }
    gst_element_release_request_pad(c->vsel, c->screen_pad);
    gst_object_unref(c->screen_pad);
    c->screen_pad = NULL;
    gst_bin_remove(GST_BIN(c->vbin), c->screen_src);
    c->screen_src = NULL;
    set_video_caps(c, c->cfg.video_w > 0 ? c->cfg.video_w : 1280, c->cfg.video_h > 0 ? c->cfg.video_h : 720, c->cfg.video_fps > 0 ? c->cfg.video_fps : 30);
    hv_call_set_bitrate(c, c->cfg.video_kbps > 0 ? c->cfg.video_kbps : 1500);
}

// --- audio compartido: todo el sistema (sin la voz del otro, gracias a la cancelación de eco)
// o solo una aplicación (target = número de serie del nodo de PipeWire). ---
int hv_call_share_audio_start(HvCall *c, const char *target, double gain) {
    if (!c->sendmix) return 0;
    if (c->sharebin) return 1;
    char g[G_ASCII_DTOSTR_BUF_SIZE];
    fmt_double(g, sizeof g, gain >= 0 ? gain : 1.0);
    char desc[1024];
    gboolean system = !target || !*target;
    if (system) {
        snprintf(desc, sizeof desc,
            "pipewiresrc stream-properties=\"props,stream.capture.sink=true\" do-timestamp=true ! "
            "audioconvert ! audioresample ! audio/x-raw,rate=48000,channels=1 ! "
            "webrtcdsp name=sharedsp echo-cancel=true noise-suppression=false gain-control=false high-pass-filter=false probe=hvprobe2 ! "
            "volume name=sharevol volume=%s ! audioconvert ! capsfilter caps=\"audio/x-raw,format=S16LE,rate=48000,channels=1\"", g);
    } else {
        snprintf(desc, sizeof desc,
            "pipewiresrc target-object=%s do-timestamp=true ! audioconvert ! audioresample ! "
            "volume name=sharevol volume=%s ! audioconvert ! capsfilter caps=\"audio/x-raw,format=S16LE,rate=48000,channels=1\"", target, g);
    }
    GstElement *bin = parse_bin("audio compartido", desc, TRUE);
    if (!bin) return 0;
    gst_bin_add(GST_BIN(c->asend), bin);
    if (!gst_element_link(bin, c->sendmix)) {
        g_warning("hilvan: no se pudo mezclar el audio compartido");
        gst_bin_remove(GST_BIN(c->asend), bin);
        return 0;
    }
    GstPad *sp = gst_element_get_static_pad(bin, "src");
    c->share_pad = gst_pad_get_peer(sp);
    gst_object_unref(sp);
    gst_element_sync_state_with_parent(bin);
    c->sharebin = bin;
    // Música y juegos: Opus en modo general y con más bitrate.
    if (c->aenc) g_object_set(c->aenc, "audio-type", 2049, "bitrate", 128000, NULL);
    return 1;
}

void hv_call_share_audio_volume(HvCall *c, double gain) {
    if (!c->sharebin) return;
    GstElement *v = child(c->sharebin, "sharevol");
    if (v) g_object_set(v, "volume", gain, NULL);
}

void hv_call_share_audio_stop(HvCall *c) {
    if (!c->sharebin) return;
    gst_element_set_locked_state(c->sharebin, TRUE);
    gst_element_set_state(c->sharebin, GST_STATE_NULL);
    if (c->share_pad) {
        GstPad *peer = gst_pad_get_peer(c->share_pad);
        if (peer) { gst_pad_unlink(peer, c->share_pad); gst_object_unref(peer); }
        gst_element_release_request_pad(c->sendmix, c->share_pad);
        gst_object_unref(c->share_pad);
        c->share_pad = NULL;
    }
    gst_bin_remove(GST_BIN(c->asend), c->sharebin);
    c->sharebin = NULL;
    if (c->aenc) g_object_set(c->aenc, "audio-type", 2048, "bitrate", c->cfg.audio_bitrate > 0 ? c->cfg.audio_bitrate : 48000, NULL);
}

// hv_call_video_sink devuelve el sink de video de un flujo ("local", "remote"...) con referencia propia.
// Su GdkPaintable hay que pedirlo desde el hilo principal de GTK (lo hace el shell).
void *hv_call_video_sink(HvCall *c, const char *stream) {
    return gst_bin_get_by_name(GST_BIN(c->pipe), stream); // busca también dentro de los bins
}

// hv_call_poll lee mensajes del bus (niveles y errores). Devuelve -1 al terminar.
int hv_call_poll(HvCall *c, int timeout_ms) {
    if (c->closed) return -1;
    GstBus *bus = gst_element_get_bus(c->pipe);
    GstMessage *msg = gst_bus_timed_pop_filtered(bus, (GstClockTime)timeout_ms * GST_MSECOND, GST_MESSAGE_ELEMENT | GST_MESSAGE_ERROR | GST_MESSAGE_WARNING);
    if (msg) {
        switch (GST_MESSAGE_TYPE(msg)) {
        case GST_MESSAGE_ELEMENT: {
            const GstStructure *s = gst_message_get_structure(msg);
            if (s && gst_structure_has_name(s, "level")) {
                const GValue *r = gst_structure_get_value(s, "rms");
                GValueArray *ra = r ? g_value_get_boxed(r) : NULL;
                if (ra && ra->n_values > 0) {
                    double rms = g_value_get_double(g_value_array_get_nth(ra, 0));
                    const char *who = GST_OBJECT_NAME(GST_MESSAGE_SRC(msg));
                    goCallLevel(c->id, (char *)(strcmp(who, "locallevel") == 0 ? "local" : "remote"), rms);
                }
            }
            break;
        }
        case GST_MESSAGE_ERROR: {
            GError *e = NULL;
            gst_message_parse_error(msg, &e, NULL);
            // Un error en la captura de pantalla o del audio compartido no corta la llamada.
            GstObject *src = GST_MESSAGE_SRC(msg);
            gboolean share = (c->screen_src && gst_object_has_as_ancestor(src, GST_OBJECT(c->screen_src))) ||
                             (c->sharebin && gst_object_has_as_ancestor(src, GST_OBJECT(c->sharebin)));
            goCallState(c->id, share ? "share-error" : "error", e ? e->message : "error");
            if (e) g_error_free(e);
            break;
        }
        default: break;
        }
        gst_message_unref(msg);
    }
    gst_object_unref(bus);
    return 0;
}

// hv_call_stats devuelve estadísticas en texto (g_free).
char *hv_call_stats(HvCall *c) {
    GstPromise *p = gst_promise_new();
    g_signal_emit_by_name(c->webrtc, "get-stats", NULL, p);
    if (gst_promise_wait(p) != GST_PROMISE_RESULT_REPLIED) { gst_promise_unref(p); return g_strdup(""); }
    const GstStructure *r = gst_promise_get_reply(p);
    char *s = r ? gst_structure_to_string(r) : g_strdup("");
    gst_promise_unref(p);
    return s;
}

void hv_call_free(HvCall *c) {
    if (!c) return;
    c->closed = TRUE;
    gst_element_set_state(c->pipe, GST_STATE_NULL);
    gst_object_unref(c->pipe);
    g_ptr_array_unref(c->uvols);
    g_free((char *)c->cfg.mic);
    g_free((char *)c->cfg.speaker);
    g_free((char *)c->cfg.camera);
    g_free(c->id);
    g_mutex_clear(&c->lock);
    g_free(c);
}

#pragma once
typedef struct {
    const char *id;
    const char *mic, *speaker, *camera;
    int video;          // enviar video
    int test_media;     // fuentes de prueba (sin dispositivos reales)
    int headless;       // sin gtk4paintablesink
    const char *stun;   // stun://host:puerto
    const char **turns; // turn(s)://usuario:clave@host:puerto?transport=udp
    int n_turns;
    int force_relay;
    int ns, ns_level, ec, agc;
    double mic_gain, spk_gain;
    int low_latency;
    int video_w, video_h, video_fps, video_kbps;
    int audio_bitrate;
    int audio_pt, video_pt;
} HvCallConfig;

typedef struct HvCall HvCall;

HvCall *hv_call_new(HvCallConfig *cfg);
int hv_call_link_sources(HvCall *c, int audio_mline, int video_mline);
int hv_call_play(HvCall *c);
char *hv_call_create(HvCall *c, int offer);
int hv_call_set_remote(HvCall *c, int offer, const char *sdp);
void hv_call_add_candidate(HvCall *c, int mline, const char *cand);
void hv_call_set_mic_muted(HvCall *c, int muted);
void hv_call_set_video_muted(HvCall *c, int muted);
void hv_call_set_volumes(HvCall *c, double mic, double spk);
void hv_call_set_bitrate(HvCall *c, int kbps);
int hv_call_screen_start(HvCall *c, int fd, unsigned int node, int fps, int w, int h, int kbps);
void hv_call_screen_quality(HvCall *c, int fps, int w, int h, int kbps);
int hv_call_share_audio_start(HvCall *c, const char *target, double gain);
void hv_call_share_audio_volume(HvCall *c, double gain);
void hv_call_share_audio_stop(HvCall *c);
int hv_call_set_input(HvCall *c, const char *dev);
int hv_call_set_output(HvCall *c, const char *dev);
int hv_call_set_camera(HvCall *c, const char *dev);
void hv_call_set_remote_volume(HvCall *c, double v);
void hv_call_screen_stop(HvCall *c);
void *hv_call_video_sink(HvCall *c, const char *stream);
int hv_call_poll(HvCall *c, int timeout_ms);
char *hv_call_stats(HvCall *c);
void hv_call_free(HvCall *c);

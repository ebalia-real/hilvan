#pragma once
void media_init(void);
char *media_list_devices(void);
#include <gst/gst.h>
GstDevice *media_find_device(const char *klass, const char *id);
GstElement *media_make_audio_source(const char *id);
GstElement *media_make_audio_sink(const char *id);
GstElement *media_make_video_source(const char *id);
int media_mictest_start(const char *mic, const char *speaker, double gain, double out_gain, int loopback, int ns, int ec, int agc, int ns_level);
void media_mictest_stop(void);
int media_mictest_poll(int timeout_ms, double *rms, double *peak);
int media_snapshot(unsigned int node, int width, int height, const char *path, int timeout_ms);
int media_video_thumb(const char *path, const char *out, int max_w, int max_h, int timeout_ms, int *w, int *h, long long *dur_ms);

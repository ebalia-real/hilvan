#pragma once
#define HILVAN_APP_ID "io.github.heelxel.Hilvan"
int shell_init(int argc, char **argv);
int shell_run(const char *home, const char *start, const char *datadir, int w, int h, int maximized, int dev, const char *initial_uri, int hidden);
void shell_present(void);
void shell_show(const char *token);
void shell_hide(void);
void shell_save_dialog(int id, const char *title, const char *name, const char *folder);
void shell_toggle(const char *token);
void shell_quit(void);
void shell_eval(const char *js);
void shell_open_uri(const char *uri);
void shell_set_title(const char *title);
void shell_set_dark(int dark);
void shell_set_background(int on);
void shell_video_attach(const char *stream, void *paintable);
void shell_video_place(const char *stream, double x, double y, double w, double h, int visible, int mirror, int contain);
void shell_video_clear(void);
void shell_window_snapshot(const char *path);

// gtkwin: Vito's own window on Linux, GTK 4 with WebKitGTK 6.0, loaded with
// dlopen so the binary keeps linking against libc alone. See gtkwin.go.
#ifndef VITO_GTKWIN_H
#define VITO_GTKWIN_H

// vw_load opens the libraries and finds every function; on failure it returns
// 0 and the missing name in *missing.
int vw_load(const char **missing);

// vw_init starts GTK (needs a display). 0 when there is none.
int vw_init(const char *app_id);

typedef struct {
	const char *url;
	const char *data_dir, *cache_dir;
	const char *scripts[8];
	int nscripts;
	int width, height, maximized;
	int own_frame; // the page draws the title bar: no GTK header bar
	int debug;
	const char *bg; // the page's background while it loads, "#rrggbb"
} vw_opts;

// vw_run makes the window and runs GTK's loop until it closes.
int vw_run(const vw_opts *o);

// Everything below runs on GTK's thread: through vw_wake from elsewhere.
void vw_wake(void);
void vw_eval(const char *js);
void vw_frame(const char *action, const char *edge);
void vw_present(const char *token);
void vw_close(void);
void vw_set_icon_name(const char *name);
void vw_set_dark(int dark);
void vw_set_radius(int px);
void vw_fix_signals(void);
// vw_sharp_cursors: GTK can show page pointers sharp on a scaled screen (4.16+).
int vw_sharp_cursors(void);
int vw_maximized(void);
int vw_active(void);
// vw_decoration_layout is GTK's gtk-decoration-layout ("appmenu:close"),
// to be freed with free().
char *vw_decoration_layout(void);

#endif

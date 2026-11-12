// gtkwin: Vito's own window on Linux. GTK 4 and WebKitGTK 6.0 are opened
// with dlopen and every function is looked up by name, so the binary never
// links against them: where they are missing, `vito window` reports it and
// Vito opens the web app or a browser tab instead (as on Windows without
// WebView2). No headers are needed to build it either; the few types and
// enums used are declared here.

#define _GNU_SOURCE
#include <dlfcn.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "gtkwin.h"
#include "_cgo_export.h"

typedef void *gp;
typedef int gboolean;
typedef unsigned long GType;
typedef unsigned int guint;
typedef unsigned long gulong;
typedef long gssize;
typedef struct { float red, green, blue, alpha; } RGBA;

// The enums, from GDK 4, GTK 4 and WebKitGTK 6.0.
enum { GDK_BUTTON_PRESS = 2 };
enum { GTK_PHASE_CAPTURE = 1 };
enum { GTK_OVERFLOW_HIDDEN = 1 };
enum { GTK_ORIENTATION_HORIZONTAL = 0 };
enum { WEBKIT_INJECT_TOP_FRAME = 1, WEBKIT_INJECT_AT_START = 0 };
enum { WEBKIT_POLICY_NAVIGATION = 0, WEBKIT_POLICY_NEW_WINDOW = 1 };
enum { G_USER_DIRECTORY_DOWNLOAD = 2 };
enum { GTK_STYLE_PROVIDER_PRIORITY_APPLICATION = 600 };

static struct {
	// GLib, GObject
	void (*g_set_prgname)(const char *);
	gulong (*g_signal_connect_data)(gp, const char *, gp, gp, gp, int);
	void (*g_object_set)(gp, const char *, ...);
	void (*g_object_get)(gp, const char *, ...);
	gp (*g_object_new)(GType, const char *, ...);
	void (*g_object_unref)(gp);
	guint (*g_idle_add)(int (*)(gp), gp);
	gp (*g_main_loop_new)(gp, gboolean);
	void (*g_main_loop_run)(gp);
	void (*g_main_loop_quit)(gp);
	void (*g_free)(gp);
	gp (*g_object_class_find_property)(gp, const char *);
	const char *(*g_get_user_special_dir)(int);
	// GTK, GDK
	gboolean (*gtk_init_check)(void);
	gp (*gtk_window_new)(void);
	void (*gtk_window_set_title)(gp, const char *);
	void (*gtk_window_set_default_size)(gp, int, int);
	void (*gtk_window_get_default_size)(gp, int *, int *);
	void (*gtk_window_set_titlebar)(gp, gp);
	void (*gtk_window_set_child)(gp, gp);
	void (*gtk_window_set_icon_name)(gp, const char *);
	void (*gtk_window_present)(gp);
	void (*gtk_window_set_startup_id)(gp, const char *);
	void (*gtk_window_maximize)(gp);
	void (*gtk_window_unmaximize)(gp);
	void (*gtk_window_minimize)(gp);
	void (*gtk_window_close)(gp);
	gboolean (*gtk_window_is_maximized)(gp);
	gboolean (*gtk_window_is_active)(gp);
	gp (*gtk_box_new)(int, int);
	void (*gtk_widget_set_size_request)(gp, int, int);
	void (*gtk_widget_add_css_class)(gp, const char *);
	void (*gtk_widget_set_overflow)(gp, int);
	gp (*gtk_widget_get_native)(gp);
	gboolean (*gtk_widget_grab_focus)(gp);
	gp (*gtk_native_get_surface)(gp);
	gp (*gtk_settings_get_default)(void);
	gp (*gtk_event_controller_legacy_new)(void);
	void (*gtk_event_controller_set_propagation_phase)(gp, int);
	void (*gtk_widget_add_controller)(gp, gp);
	gp (*gtk_css_provider_new)(void);
	void (*gtk_css_provider_load_from_data)(gp, const char *, gssize);
	void (*gtk_style_context_add_provider_for_display)(gp, gp, guint);
	gp (*gdk_display_get_default)(void);
	int (*gdk_event_get_event_type)(gp);
	gp (*gdk_event_get_device)(gp);
	gboolean (*gdk_event_get_position)(gp, double *, double *);
	uint32_t (*gdk_event_get_time)(gp);
	guint (*gdk_button_event_get_button)(gp);
	gp (*gdk_event_ref)(gp);
	void (*gdk_event_unref)(gp);
	void (*gdk_toplevel_begin_move)(gp, gp, int, double, double, uint32_t);
	void (*gdk_toplevel_begin_resize)(gp, int, gp, int, double, double, uint32_t);
	gboolean (*gdk_toplevel_show_window_menu)(gp, gp);
	// WebKitGTK, JavaScriptCore
	GType (*webkit_web_view_get_type)(void);
	gp (*webkit_network_session_new)(const char *, const char *);
	gp (*webkit_user_content_manager_new)(void);
	gp (*webkit_user_script_new)(const char *, int, int, const char *const *, const char *const *);
	void (*webkit_user_script_unref)(gp);
	void (*webkit_user_content_manager_add_script)(gp, gp);
	gboolean (*webkit_user_content_manager_register_script_message_handler)(gp, const char *, const char *);
	gp (*webkit_web_view_get_settings)(gp);
	void (*webkit_settings_set_enable_developer_extras)(gp, gboolean);
	void (*webkit_settings_set_javascript_can_access_clipboard)(gp, gboolean);
	void (*webkit_web_view_set_background_color)(gp, const RGBA *);
	void (*webkit_web_view_load_uri)(gp, const char *);
	void (*webkit_web_view_reload)(gp);
	void (*webkit_web_view_evaluate_javascript)(gp, const char *, gssize, const char *, const char *, gp, gp, gp);
	void (*webkit_permission_request_allow)(gp);
	void (*webkit_policy_decision_ignore)(gp);
	gp (*webkit_navigation_policy_decision_get_navigation_action)(gp);
	gp (*webkit_navigation_action_get_request)(gp);
	int (*webkit_navigation_action_get_navigation_type)(gp);
	const char *(*webkit_uri_request_get_uri)(gp);
	void (*webkit_download_set_destination)(gp, const char *);
	const char *(*webkit_download_get_destination)(gp);
	char *(*jsc_value_to_string)(gp);
	// Optional: GTK 4.16 and later (sharp pointers on a scaled screen).
	gp (*gdk_cursor_new_from_callback)(gp, gp, gp, gp);
	gp (*gdk_cursor_get_texture)(gp);
	int (*gdk_cursor_get_hotspot_x)(gp);
	int (*gdk_cursor_get_hotspot_y)(gp);
	gp (*gdk_cursor_get_fallback)(gp);
	int (*gdk_texture_get_width)(gp);
	int (*gdk_texture_get_height)(gp);
	gp (*gtk_widget_get_cursor)(gp);
	void (*gtk_widget_set_cursor)(gp, gp);
	int (*gtk_widget_get_scale_factor)(gp);
	gp (*g_object_ref)(gp);
} F;

static const char *libs[] = {"libgtk-4.so.1", "libjavascriptcoregtk-6.0.so.1", "libwebkitgtk-6.0.so.4"};

int vw_load(const char **missing) {
	static int loaded;
	if (loaded) {
		return 1;
	}
	for (size_t i = 0; i < sizeof libs / sizeof libs[0]; i++) {
		if (!dlopen(libs[i], RTLD_NOW | RTLD_GLOBAL)) {
			*missing = libs[i];
			return 0;
		}
	}
#define L(name)                                                     \
	if (!(*(void **)&F.name = dlsym(RTLD_DEFAULT, #name))) {        \
		*missing = #name;                                           \
		return 0;                                                   \
	}
	L(g_set_prgname) L(g_signal_connect_data) L(g_object_set) L(g_object_get) L(g_object_new)
	L(g_object_unref) L(g_idle_add) L(g_main_loop_new) L(g_main_loop_run) L(g_main_loop_quit)
	L(g_free) L(g_get_user_special_dir) L(g_object_class_find_property)
	L(gtk_init_check) L(gtk_window_new) L(gtk_window_set_title) L(gtk_window_set_default_size)
	L(gtk_window_get_default_size) L(gtk_window_set_titlebar) L(gtk_window_set_child)
	L(gtk_window_set_icon_name) L(gtk_window_present) L(gtk_window_set_startup_id)
	L(gtk_window_maximize) L(gtk_window_unmaximize) L(gtk_window_minimize) L(gtk_window_close)
	L(gtk_window_is_maximized) L(gtk_window_is_active) L(gtk_box_new) L(gtk_widget_set_size_request)
	L(gtk_widget_add_css_class) L(gtk_widget_set_overflow) L(gtk_widget_get_native)
	L(gtk_widget_grab_focus) L(gtk_native_get_surface) L(gtk_settings_get_default)
	L(gtk_event_controller_legacy_new) L(gtk_event_controller_set_propagation_phase)
	L(gtk_widget_add_controller) L(gtk_css_provider_new) L(gtk_css_provider_load_from_data)
	L(gtk_style_context_add_provider_for_display) L(gdk_display_get_default)
	L(gdk_event_get_event_type) L(gdk_event_get_device) L(gdk_event_get_position)
	L(gdk_event_get_time) L(gdk_button_event_get_button) L(gdk_event_ref) L(gdk_event_unref)
	L(gdk_toplevel_begin_move) L(gdk_toplevel_begin_resize) L(gdk_toplevel_show_window_menu)
	L(webkit_web_view_get_type) L(webkit_network_session_new) L(webkit_user_content_manager_new)
	L(webkit_user_script_new) L(webkit_user_script_unref) L(webkit_user_content_manager_add_script)
	L(webkit_user_content_manager_register_script_message_handler) L(webkit_web_view_get_settings)
	L(webkit_settings_set_enable_developer_extras) L(webkit_settings_set_javascript_can_access_clipboard)
	L(webkit_web_view_set_background_color) L(webkit_web_view_load_uri) L(webkit_web_view_reload)
	L(webkit_web_view_evaluate_javascript) L(webkit_permission_request_allow)
	L(webkit_policy_decision_ignore) L(webkit_navigation_policy_decision_get_navigation_action)
	L(webkit_navigation_action_get_request) L(webkit_navigation_action_get_navigation_type)
	L(webkit_uri_request_get_uri) L(webkit_download_set_destination)
	L(webkit_download_get_destination) L(jsc_value_to_string)
#undef L
#define O(name) *(void **)&F.name = dlsym(RTLD_DEFAULT, #name);
	O(gdk_cursor_new_from_callback) O(gdk_cursor_get_texture) O(gdk_cursor_get_hotspot_x)
	O(gdk_cursor_get_hotspot_y) O(gdk_cursor_get_fallback) O(gdk_texture_get_width)
	O(gdk_texture_get_height) O(gtk_widget_get_cursor) O(gtk_widget_set_cursor)
	O(gtk_widget_get_scale_factor) O(g_object_ref)
#undef O
	loaded = 1;
	return 1;
}

int vw_init(const char *app_id) {
	// The app id names the window to the desktop (Wayland's app_id, X11's
	// WM_CLASS): the dock shows Vito's desktop entry and icon for it.
	F.g_set_prgname(app_id);
	int ok = F.gtk_init_check();
	vw_fix_signals();
	return ok;
}

static gp win, view, loop, press, css;
static int dark_now = -1, radius_now = -1;

#define CONNECT(obj, sig, cb) F.g_signal_connect_data(obj, sig, (gp)(cb), NULL, NULL, 0)

static int on_idle(gp data) {
	(void)data;
	goVwQueue();
	return 0; // G_SOURCE_REMOVE
}

void vw_wake(void) { F.g_idle_add(on_idle, NULL); }

void vw_eval(const char *js) {
	if (view) {
		F.webkit_web_view_evaluate_javascript(view, js, -1, NULL, NULL, NULL, NULL, NULL);
	}
}

// on_event keeps the last button press: dragging, resizing and the window
// menu are started from it, as the desktop wants them tied to a real press.
static gboolean on_event(gp ctrl, gp ev, gp data) {
	(void)ctrl;
	(void)data;
	if (F.gdk_event_get_event_type(ev) == GDK_BUTTON_PRESS) {
		if (press) {
			F.gdk_event_unref(press);
		}
		press = F.gdk_event_ref(ev);
	}
	return 0;
}

void vw_frame(const char *a, const char *edge) {
	if (!win) {
		return;
	}
	if (!strcmp(a, "min")) {
		F.gtk_window_minimize(win);
		return;
	}
	if (!strcmp(a, "max")) {
		if (F.gtk_window_is_maximized(win)) {
			F.gtk_window_unmaximize(win);
		} else {
			F.gtk_window_maximize(win);
		}
		return;
	}
	if (!strcmp(a, "close")) {
		F.gtk_window_close(win);
		return;
	}
	gp surface = F.gtk_native_get_surface(F.gtk_widget_get_native(win));
	if (!surface || !press) {
		return;
	}
	double x = 0, y = 0;
	F.gdk_event_get_position(press, &x, &y);
	gp dev = F.gdk_event_get_device(press);
	int button = (int)F.gdk_button_event_get_button(press);
	uint32_t t = F.gdk_event_get_time(press);
	if (!strcmp(a, "drag")) {
		F.gdk_toplevel_begin_move(surface, dev, button, x, y, t);
	} else if (!strcmp(a, "resize") && !F.gtk_window_is_maximized(win)) {
		int e = !strcmp(edge, "topleft") ? 0 : !strcmp(edge, "topright") ? 2 : 1; // GDK_SURFACE_EDGE_*
		F.gdk_toplevel_begin_resize(surface, e, dev, button, x, y, t);
	} else if (!strcmp(a, "menu")) {
		F.gdk_toplevel_show_window_menu(surface, press);
	}
}

void vw_present(const char *token) {
	if (!win) {
		return;
	}
	if (token && *token) {
		F.gtk_window_set_startup_id(win, token);
	}
	F.gtk_window_present(win);
}

void vw_set_icon_name(const char *name) {
	if (win) {
		F.gtk_window_set_icon_name(win, name);
	}
}

void vw_close(void) {
	if (win) {
		F.gtk_window_close(win);
	}
}

int vw_maximized(void) { return win && F.gtk_window_is_maximized(win); }
int vw_active(void) { return win && F.gtk_window_is_active(win); }

void vw_set_dark(int dark) {
	if (dark == dark_now) {
		return;
	}
	dark_now = dark;
	// Context menus, the file chooser and the window's border follow it.
	// GTK 4.20 names it gtk-interface-color-scheme (2 dark, 3 light).
	gp settings = F.gtk_settings_get_default();
	if (F.g_object_class_find_property(*(gp *)settings, "gtk-interface-color-scheme")) {
		F.g_object_set(settings, "gtk-interface-color-scheme", dark ? 2 : 3, NULL);
	} else {
		F.g_object_set(settings, "gtk-application-prefer-dark-theme", dark, NULL);
	}
}

// vw_fix_signals adds SA_ONSTACK to the signal handlers GTK and JavaScriptCore
// installed (JSC takes SIGUSR1 for its garbage collector): Go runs its
// goroutines on small stacks and needs every handler on the alternate one, or
// a signal landing on a Go thread crashes the process.
void vw_fix_signals(void) {
	for (int s = 1; s < NSIG; s++) {
		struct sigaction sa;
		if (sigaction(s, NULL, &sa) != 0 || sa.sa_handler == SIG_DFL || sa.sa_handler == SIG_IGN) {
			continue;
		}
		if (!(sa.sa_flags & SA_ONSTACK)) {
			sa.sa_flags |= SA_ONSTACK;
			sigaction(s, &sa, NULL);
		}
	}
}

// vw_set_radius rounds the window's corners as ui.radius rounds the page's;
// maximised, tiled or full screen it has none.
void vw_set_radius(int px) {
	if (px == radius_now || !css) {
		return;
	}
	radius_now = px;
	char buf[1024];
	snprintf(buf, sizeof buf,
		"window.vito-win { border-radius: %dpx; }\n"
		"window.vito-win.maximized, window.vito-win.fullscreen, window.vito-win.tiled,"
		" window.vito-win.tiled-top, window.vito-win.tiled-bottom, window.vito-win.tiled-left,"
		" window.vito-win.tiled-right { border-radius: 0; }\n"
		"window.vito-win > .titlebar { min-height: 0; padding: 0; margin: 0; border: none;"
		" background: none; box-shadow: none; }\n",
		px);
	F.gtk_css_provider_load_from_data(css, buf, -1);
}

char *vw_decoration_layout(void) {
	char *s = NULL;
	F.g_object_get(F.gtk_settings_get_default(), "gtk-decoration-layout", &s, NULL);
	char *out = strdup(s ? s : "");
	if (s) {
		F.g_free(s);
	}
	return out;
}

static void on_state(gp obj, gp pspec, gp data) {
	(void)obj;
	(void)pspec;
	(void)data;
	goVwState();
}

static gboolean on_close_request(gp w, gp data) {
	(void)data;
	int width = 0, height = 0;
	F.gtk_window_get_default_size(w, &width, &height);
	goVwClosing(width, height, F.gtk_window_is_maximized(w));
	return 0; // let it close
}

static void on_destroy(gp w, gp data) {
	(void)w;
	(void)data;
	win = NULL;
	view = NULL;
	F.g_main_loop_quit(loop);
}

static void on_message(gp ucm, gp value, gp data) {
	(void)ucm;
	(void)data;
	static int fixed;
	if (!fixed) { // JavaScriptCore is surely set up by the first message
		vw_fix_signals();
		fixed = 1;
	}
	char *s = F.jsc_value_to_string(value);
	if (s) {
		goVwMessage(s);
		F.g_free(s);
	}
}

// on_policy keeps the window on Vito's page: links to other sites and new
// windows go to the browser (Go decides, goVwNavigate).
static gboolean on_policy(gp v, gp decision, int type, gp data) {
	(void)v;
	(void)data;
	if (type != WEBKIT_POLICY_NAVIGATION && type != WEBKIT_POLICY_NEW_WINDOW) {
		return 0;
	}
	gp action = F.webkit_navigation_policy_decision_get_navigation_action(decision);
	if (!action) {
		return 0;
	}
	const char *uri = F.webkit_uri_request_get_uri(F.webkit_navigation_action_get_request(action));
	int link = type == WEBKIT_POLICY_NEW_WINDOW || F.webkit_navigation_action_get_navigation_type(action) == 0;
	if (uri && goVwNavigate((char *)uri, link)) {
		F.webkit_policy_decision_ignore(decision);
		return 1;
	}
	return 0;
}

// Only Vito's own page runs here: what it asks for (the clipboard, chiefly)
// is granted.
static gboolean on_permission(gp v, gp req, gp data) {
	(void)v;
	(void)data;
	F.webkit_permission_request_allow(req);
	return 1;
}

static void on_crashed(gp v, int reason, gp data) {
	(void)reason;
	(void)data;
	F.webkit_web_view_reload(v);
}

// Downloads (exports, a recording) land in the Downloads folder under a name
// not taken yet.
static gboolean on_destination(gp dl, const char *suggested, gp data) {
	(void)data;
	const char *dir = F.g_get_user_special_dir(G_USER_DIRECTORY_DOWNLOAD);
	if (!dir) {
		dir = getenv("HOME");
	}
	if (!dir || !suggested || !*suggested || strchr(suggested, '/')) {
		return 0;
	}
	char path[4096];
	snprintf(path, sizeof path, "%s/%s", dir, suggested);
	const char *dot = strrchr(suggested, '.');
	size_t stem = dot && dot != suggested ? (size_t)(dot - suggested) : strlen(suggested);
	for (int i = 1; access(path, F_OK) == 0 && i < 1000; i++) {
		snprintf(path, sizeof path, "%s/%.*s (%d)%s", dir, (int)stem, suggested, i, suggested + stem);
	}
	F.webkit_download_set_destination(dl, path);
	return 1;
}

static void on_finished(gp dl, gp data) {
	(void)data;
	const char *dest = F.webkit_download_get_destination(dl);
	if (dest) {
		goVwDownloaded((char *)dest);
	}
}

static void on_download(gp session, gp dl, gp data) {
	(void)session;
	(void)data;
	CONNECT(dl, "decide-destination", on_destination);
	CONNECT(dl, "finished", on_finished);
}

// ---- sharp pointers on a scaled screen ----
// WebKit hands GTK a page's pointer image (Vito's own) drawn at the screen's
// scale, 64 px at 2×, and GTK shows a texture cursor at its size in logical
// pixels: twice as large. A callback cursor (GTK 4.16) says how large the
// texture is meant to be, so GTK shrinks the 64 px to the 48 a 1.5× screen
// needs instead of stretching 32 px to it.

typedef struct {
	gp texture;
	int w, h, hx, hy; // logical
} cursor_data;

static gp on_cursor_texture(gp cursor, int size, double scale, int *w, int *h, int *hx, int *hy, gp data) {
	(void)cursor;
	(void)size;
	(void)scale;
	cursor_data *d = data;
	*w = d->w;
	*h = d->h;
	*hx = d->hx;
	*hy = d->hy;
	return F.g_object_ref(d->texture);
}

static void free_cursor_data(gp data) {
	cursor_data *d = data;
	F.g_object_unref(d->texture);
	free(d);
}

int vw_sharp_cursors(void) {
	return F.gdk_cursor_new_from_callback && F.gdk_cursor_get_texture && F.gdk_cursor_get_hotspot_x &&
		F.gdk_cursor_get_hotspot_y && F.gdk_cursor_get_fallback && F.gdk_texture_get_width &&
		F.gdk_texture_get_height && F.gtk_widget_get_cursor && F.gtk_widget_set_cursor &&
		F.gtk_widget_get_scale_factor && F.g_object_ref;
}

static gp seen_cursor, made_cursor; // the last one WebKit set, and ours for it

static void on_cursor(gp widget, gp pspec, gp data) {
	(void)pspec;
	(void)data;
	gp c = F.gtk_widget_get_cursor(widget);
	if (!c || c == made_cursor) {
		return;
	}
	gp tex = F.gdk_cursor_get_texture(c);
	int k = F.gtk_widget_get_scale_factor(widget);
	if (!tex || k <= 1) {
		return; // a named cursor, or a screen at 1×: as it is
	}
	if (c != seen_cursor) {
		cursor_data *d = calloc(1, sizeof *d);
		d->texture = F.g_object_ref(tex);
		d->w = (F.gdk_texture_get_width(tex) + k - 1) / k;
		d->h = (F.gdk_texture_get_height(tex) + k - 1) / k;
		d->hx = F.gdk_cursor_get_hotspot_x(c) / k;
		d->hy = F.gdk_cursor_get_hotspot_y(c) / k;
		if (made_cursor) {
			F.g_object_unref(made_cursor);
		}
		if (seen_cursor) {
			F.g_object_unref(seen_cursor);
		}
		seen_cursor = F.g_object_ref(c);
		made_cursor = F.gdk_cursor_new_from_callback((gp)on_cursor_texture, d, (gp)free_cursor_data,
			F.gdk_cursor_get_fallback(c));
	}
	F.gtk_widget_set_cursor(widget, made_cursor);
}

static RGBA parse_rgb(const char *hex) {
	RGBA c = {1, 1, 1, 1};
	unsigned r, g, b;
	if (hex && sscanf(hex, "#%02x%02x%02x", &r, &g, &b) == 3) {
		c.red = r / 255.f;
		c.green = g / 255.f;
		c.blue = b / 255.f;
	}
	return c;
}

int vw_run(const vw_opts *o) {
	win = F.gtk_window_new();
	F.gtk_window_set_title(win, "Vito");
	F.gtk_window_set_icon_name(win, "vito");
	F.gtk_widget_add_css_class(win, "vito-win");
	F.gtk_widget_set_size_request(win, 420, 480); // the page's phone layout fits
	F.gtk_window_set_default_size(win, o->width, o->height);
	// No title bar of GTK's: the page draws its own where the desktop shows
	// them (own_frame), and none at all on a tiling desktop such as niri,
	// where windows have none. GTK keeps the frame: resize borders, shadow.
	F.gtk_window_set_titlebar(win, F.gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 0));
	F.gtk_widget_set_overflow(win, GTK_OVERFLOW_HIDDEN); // the corners clip the page

	css = F.gtk_css_provider_new();
	F.gtk_style_context_add_provider_for_display(F.gdk_display_get_default(), css,
		GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
	vw_set_radius(12);

	gp session = F.webkit_network_session_new(o->data_dir, o->cache_dir);
	gp ucm = F.webkit_user_content_manager_new();
	for (int i = 0; i < o->nscripts; i++) {
		gp s = F.webkit_user_script_new(o->scripts[i], WEBKIT_INJECT_TOP_FRAME, WEBKIT_INJECT_AT_START, NULL, NULL);
		F.webkit_user_content_manager_add_script(ucm, s);
		F.webkit_user_script_unref(s);
	}
	F.webkit_user_content_manager_register_script_message_handler(ucm, "vito", NULL);
	CONNECT(ucm, "script-message-received::vito", on_message);
	CONNECT(session, "download-started", on_download);

	view = F.g_object_new(F.webkit_web_view_get_type(),
		"network-session", session, "user-content-manager", ucm, NULL);
	gp settings = F.webkit_web_view_get_settings(view);
	F.webkit_settings_set_enable_developer_extras(settings, o->debug);
	F.webkit_settings_set_javascript_can_access_clipboard(settings, 1);
	RGBA bg = parse_rgb(o->bg);
	F.webkit_web_view_set_background_color(view, &bg);
	CONNECT(view, "decide-policy", on_policy);
	CONNECT(view, "permission-request", on_permission);
	CONNECT(view, "web-process-terminated", on_crashed);
	if (vw_sharp_cursors()) {
		CONNECT(view, "notify::cursor", on_cursor);
	}
	F.gtk_window_set_child(win, view);

	gp legacy = F.gtk_event_controller_legacy_new();
	F.gtk_event_controller_set_propagation_phase(legacy, GTK_PHASE_CAPTURE);
	CONNECT(legacy, "event", on_event);
	F.gtk_widget_add_controller(win, legacy);

	CONNECT(win, "notify::maximized", on_state);
	CONNECT(win, "notify::is-active", on_state);
	CONNECT(win, "close-request", on_close_request);
	CONNECT(win, "destroy", on_destroy);

	vw_fix_signals();
	F.webkit_web_view_load_uri(view, o->url);
	if (o->maximized) {
		F.gtk_window_maximize(win);
	}
	loop = F.g_main_loop_new(NULL, 0);
	F.gtk_window_present(win);
	F.gtk_widget_grab_focus(view);
	F.g_main_loop_run(loop);
	return 0;
}
